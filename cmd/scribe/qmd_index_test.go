package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scriptedQMD puts a `qmd` on PATH whose behavior is the given /bin/sh case
// body over "$1 $2", so a test can answer `collection list`, `collection
// show <name>`, `ls` and `update` differently.
func scriptedQMD(t *testing.T, caseBody string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" + caseBody + "\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "qmd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestQMDCollectionNames(t *testing.T) {
	got := qmdCollectionNames(qmdCollectionListFixture)
	if !slices.Equal(got, []string{"scriptorium", "enaia"}) {
		t.Errorf("qmdCollectionNames = %v, want [scriptorium enaia]", got)
	}
}

func TestParseQMDAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"45s ago": 45 * time.Second, "3m ago": 3 * time.Minute,
		"2h ago": 2 * time.Hour, "17d ago": 17 * 24 * time.Hour,
	} {
		if got, ok := parseQMDAge(in); !ok || got != want {
			t.Errorf("parseQMDAge(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "never", "3w ago", "d ago", "x2h ago"} {
		if _, ok := parseQMDAge(in); ok {
			t.Errorf("parseQMDAge(%q) parsed, want rejected", in)
		}
	}
}

func TestQMDUpdateCoveredRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cases := []struct {
		name           string
		out            string
		covered, known bool
	}{
		{"covered among others", "Updating 2 collection(s)...\nCollection: " + other + " (**/*.md)\nCollection: " + root + " (**/*.md)\n", true, true},
		{"only another KB", "Collection: " + other + " (**/*.md)\n", false, true},
		{"none registered", "No collections found. Run 'qmd collection add .' to index markdown files.", false, true},
		{"colored", "\x1b[2mCollection: " + root + " (**/*.md)\x1b[0m", true, true},
		{"unfamiliar output", "ok", false, false},
	}
	for _, c := range cases {
		covered, known := qmdUpdateCoveredRoot(c.out, root)
		if covered != c.covered || known != c.known {
			t.Errorf("%s: got covered=%v known=%v, want %v %v", c.name, covered, known, c.covered, c.known)
		}
	}
}

// `qmd update` exits 0 when no collection covers the KB. That clean exit
// used to count as a successful reindex of a KB qmd never looked at.
func TestReindexQMDFailsWhenNoCollectionCoversKB(t *testing.T) {
	t.Setenv("SCRIBE_SKIP_REINDEX", "")
	root := t.TempDir()
	scriptedQMD(t, `"update ") echo "Collection: /somewhere/else (**/*.md)" ;;
*) echo "done" ;;`)
	if err := reindexQMD(root, "test"); !errors.Is(err, errQMDNoCollection) {
		t.Errorf("reindexQMD = %v, want errQMDNoCollection", err)
	}

	scriptedQMD(t, `"update ") echo "Collection: `+root+` (**/*.md)" ;;
*) echo "done" ;;`)
	if err := reindexQMD(root, "test"); err != nil {
		t.Errorf("reindexQMD = %v, want nil when the KB's collection was updated", err)
	}
}

// A collection registered under a name that is not the folder's basename
// (enaia for enaia-kb) is still found, by path. Status used to report
// such a KB as "not indexed yet".
func TestQMDCollectionForMatchesByPathNotName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "enaia-kb")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptedQMD(t, `"collection list") printf 'Collections (2):\n\nenaia-kb (qmd://enaia-kb/)\n  Files:    5\n  Updated:  1h ago\n\nenaia (qmd://enaia/) [excluded]\n  Files:    1913\n  Updated:  2h ago\n' ;;
"collection show") if [ "$3" = enaia ]; then printf 'Collection: enaia\n  Path:     `+root+`\n'; else printf 'Collection: %s\n  Path:     /elsewhere\n' "$3"; fi ;;`)

	col, ok := qmdCollectionFor(root)
	if !ok || col.Name != "enaia" || col.Files != "1913" || col.Updated != "2h ago" {
		t.Errorf("qmdCollectionFor = %+v, %v; want the enaia collection (same-named enaia-kb points elsewhere)", col, ok)
	}
}

func TestCheckLiteralTildePATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	c, bad := checkLiteralTildePATH("/usr/bin" + sep + "~/.local/bin")
	if !bad || c.Status != statusWarn || !strings.Contains(c.Detail, "~/.local/bin") {
		t.Errorf("check = %+v, %v; want a WARN naming the ~ entry", c, bad)
	}
	if _, bad := checkLiteralTildePATH("/usr/bin" + sep + "/Users/x/.local/bin"); bad {
		t.Error("expanded PATH flagged")
	}
}

// qmdIndexKB builds a git KB with one committed markdown file.
func qmdIndexKB(t *testing.T) string {
	t.Helper()
	root := initTestGitRepo(t, "QMD Tester")
	writeTestArticle(t, root, "wiki/a.md", "# a\n")
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "a", "--no-gpg-sign"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func qmdIndexScript(root, updated, lsOut string) string {
	return fmt.Sprintf(`"collection list") printf 'kb (qmd://kb/)\n  Files:    3\n  Updated:  %s\n' ;;
"collection show") printf 'Collection: kb\n  Path:     %s\n' ;;
"ls kb/output") printf '%s' ;;`, updated, root, lsOut)
}

func TestCheckQMDIndex(t *testing.T) {
	root := qmdIndexKB(t)
	now := time.Now()

	scriptedQMD(t, qmdIndexScript(root, "1h ago", "No files found under qmd://kb/output"))
	got := checkQMDIndex(root, now)
	if len(got) != 1 || got[0].Status != statusOK {
		t.Fatalf("fresh index: %+v, want one OK row", got)
	}

	// Index last updated 5 days before the newest markdown commit.
	scriptedQMD(t, qmdIndexScript(root, "5d ago", ""))
	got = checkQMDIndex(root, now)
	if len(got) != 1 || got[0].Status != statusWarn || !strings.Contains(got[0].Detail, "behind") {
		t.Errorf("stale index: %+v, want a WARN saying how far behind", got)
	}

	scriptedQMD(t, qmdIndexScript(root, "1h ago", `1 KB  qmd://kb/output/drops-x/a.md\n1 KB  qmd://kb/output/drops-x/b.md\n`))
	got = checkQMDIndex(root, now)
	if len(got) != 2 || got[1].Name != "qmd scope" || !strings.Contains(got[1].Detail, "at least 2 files") {
		t.Errorf("output indexed: %+v, want a qmd scope WARN counting 2 files", got)
	}

	scriptedQMD(t, `"collection list") printf 'other (qmd://other/)\n' ;;
"collection show") printf 'Collection: other\n  Path:     /elsewhere\n' ;;`)
	got = checkQMDIndex(root, now)
	if len(got) != 1 || got[0].Status != statusWarn || !strings.Contains(got[0].Fix, "qmd collection add") {
		t.Errorf("no collection: %+v, want a WARN with the collection add fix", got)
	}

	scriptedQMD(t, `*) exit 1 ;;`)
	if got := checkQMDIndex(root, now); len(got) != 0 {
		t.Errorf("qmd unavailable: %+v, want silence (deps reports it)", got)
	}
}
