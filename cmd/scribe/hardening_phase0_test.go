package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the 2026-10-15 security audit, Phase 0: the
// small deterministic fixes (see .claude/research/2026-10-15-security-fix-plan.md).

func TestStopWordsRedactionIsLiteral(t *testing.T) {
	mask := compileStopWords([]string{"codename"})
	// "$0" is the regexp template for "the whole match": a pushed
	// redaction string of "$0" used to put the masked word straight back.
	dec := applyStopWords([]byte("project codename shipped\n"), nil, mask, "$0")
	if !dec.masked {
		t.Fatal("mask did not fire")
	}
	if strings.Contains(string(dec.content), "codename") {
		t.Fatalf("masked word leaked through template expansion: %q", dec.content)
	}
	if !strings.Contains(string(dec.content), "$0") {
		t.Fatalf("redaction must be inserted literally, got %q", dec.content)
	}
}

func TestStopWordsUserRedactionWinsOverShared(t *testing.T) {
	seedStopWords(t, "stop_words:\n  redaction: \"[mine]\"\n")
	cfg := &ScribeConfig{StopWords: StopWordsConfig{Redaction: "[theirs]"}}
	_, _, redaction := stopWordRules(cfg)
	if redaction != "[mine]" {
		t.Fatalf("personal redaction must win over the repo value, got %q", redaction)
	}
	seedStopWords(t, "")
	_, _, redaction = stopWordRules(cfg)
	if redaction != "[theirs]" {
		t.Fatalf("repo redaction applies when no personal one is set, got %q", redaction)
	}
}

func TestWatchJobQuotesBinaryPath(t *testing.T) {
	jobs := scribeJobs("/Users/Some One/.local/bin/scribe")
	for _, j := range jobs {
		if j.Name != "watch" {
			continue
		}
		if !strings.HasPrefix(j.Command, "'/Users/Some One/.local/bin/scribe'") {
			t.Fatalf("watch command must shell-quote the binary: %q", j.Command)
		}
		return
	}
	t.Fatal("no watch job")
}

func TestGitChangedFilesRejectsOptionShapedSHA(t *testing.T) {
	repo := buildIgnoreRepo(t)
	for _, bad := range []string{"--output=/tmp/pwn", "-c core.pager=id", "HEAD~1", "abc"} {
		got := gitChangedFiles(repo, bad, extractScanPatterns)
		// Falls back to the full tracked listing rather than running git
		// with an attacker-shaped argument before `--`.
		if len(got) == 0 {
			t.Fatalf("%q: expected full-listing fallback, got nothing", bad)
		}
		if _, err := os.Stat("/tmp/pwn"); err == nil {
			t.Fatalf("git honored %q as an option", bad)
		}
	}
	head := gitSHA(repo)
	if !gitSHARE.MatchString(head) {
		t.Fatalf("real SHA %q must pass the shape check", head)
	}
}

func TestScanFindsRealKeyAfterAllowlistedPlaceholder(t *testing.T) {
	line := "example AKIAIOSFODNN7EXAMPLE then real " + fakeAWSKey()
	if hits := scanLine(t, line, false); len(hits) != 1 {
		t.Fatalf("real key hidden behind an allowlisted placeholder on the same line: %+v", hits)
	}
}

func TestGateTextFileCoversDotenvAndToml(t *testing.T) {
	for _, rel := range []string{".env", "api/.env.local", "config/.ENV.production", "scripts/x.toml", "wiki/a.md"} {
		if !isGateTextFile(rel) {
			t.Errorf("%s must be scanned", rel)
		}
	}
	for _, rel := range []string{"bin/scribe", "img/x.png", "envelope", "a.environment"} {
		if isGateTextFile(rel) {
			t.Errorf("%s must not be scanned", rel)
		}
	}
}

func TestSecretGateActivePredicate(t *testing.T) {
	cases := []struct {
		name string
		cfg  *ScribeConfig
		want bool
	}{
		{"nil", nil, false},
		{"solo default off", &ScribeConfig{}, false},
		{"solo opt-in", &ScribeConfig{SecretScan: SecretScanConfig{Enable: true}}, true},
		{"team default on", &ScribeConfig{Team: true}, true},
		{"team disabled", &ScribeConfig{Team: true, SecretScan: SecretScanConfig{Disable: true}}, false},
		{"solo enable+disable → off", &ScribeConfig{SecretScan: SecretScanConfig{Enable: true, Disable: true}}, false},
	}
	for _, tc := range cases {
		if got := secretGateActive(tc.cfg); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestErrorRecordTailsAreRedacted(t *testing.T) {
	root := t.TempDir()
	appendErrorRecord(root, ErrorRecord{
		Op:         "x",
		ErrKind:    "other",
		Err:        `Get "https://api.example/v1?auth_token=user:HEX123": boom`,
		StderrTail: "fetch https://h/x?api_key=SECRET failed",
	})
	files, _ := filepath.Glob(filepath.Join(root, "output", "errors", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected one error log, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if strings.Contains(string(data), "HEX123") || strings.Contains(string(data), "SECRET") {
		t.Fatalf("token leaked into error record: %s", data)
	}
}
