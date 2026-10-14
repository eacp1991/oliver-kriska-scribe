package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeExec(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A plain string sort puts v9 above v26 ("v2" < "v9"), which would pick a
// years-old node install over the current one.
func TestVersionNumbersOrdersNumericallyNotLexically(t *testing.T) {
	newer := "/h/.nvm/versions/node/v26.4.0/bin/qmd"
	older := "/h/.nvm/versions/node/v9.0.0/bin/qmd"
	if got := compareVersionPaths(newer, older); got <= 0 {
		t.Errorf("compareVersionPaths(v26.4.0, v9.0.0) = %d, want > 0", got)
	}
	if newer < older {
		t.Log("confirmed: naive string comparison would have picked v9.0.0")
	}
	if got := versionNumbers(newer); len(got) != 3 || got[0] != 26 || got[1] != 4 || got[2] != 0 {
		t.Errorf("versionNumbers = %v, want [26 4 0]", got)
	}
}

func TestNewestExecutableMatchSkipsNonExecutableAndPicksNewest(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".nvm", "versions", "node")
	writeExec(t, filepath.Join(base, "v20.1.0", "bin", "qmd"))
	want := writeExec(t, filepath.Join(base, "v26.4.0", "bin", "qmd"))

	// A non-executable file at a newer version must be ignored: an npm
	// leftover is not a runnable shim.
	stub := filepath.Join(base, "v30.0.0", "bin", "qmd")
	if err := os.MkdirAll(filepath.Dir(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := newestExecutableMatch(filepath.Join(base, "*", "bin", "qmd"))
	if got != want {
		t.Errorf("newestExecutableMatch = %q, want %q", got, want)
	}
}

// The regression: qmd installed only under nvm, nothing on PATH, which is
// exactly what a LaunchAgent sees.
func TestResolveQMDBinaryProbesNodeManagersWhenPATHIsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // no qmd anywhere on PATH

	want := writeExec(t, filepath.Join(home, ".nvm", "versions", "node", "v26.4.0", "bin", "qmd"))
	if got := resolveQMDBinaryWith(""); got != want {
		t.Errorf("resolveQMDBinaryWith = %q, want %q", got, want)
	}
}

func TestResolveQMDBinaryPrefersExplicitConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	writeExec(t, filepath.Join(home, ".nvm", "versions", "node", "v26.4.0", "bin", "qmd"))
	explicit := writeExec(t, filepath.Join(t.TempDir(), "custom-qmd"))

	if got := resolveQMDBinaryWith(explicit); got != explicit {
		t.Errorf("resolveQMDBinaryWith(%q) = %q, want the explicit path", explicit, got)
	}
	// A configured path that does not exist must not win — fall through
	// rather than exec something that isn't there.
	if got := resolveQMDBinaryWith(filepath.Join(home, "nope", "qmd")); got == filepath.Join(home, "nope", "qmd") {
		t.Error("a nonexistent qmd_path was returned instead of falling through to the prober")
	}
}

func TestResolveQMDBinaryFallsBackToBareName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	skipIfSystemQMD(t)
	if got := resolveQMDBinaryWith(""); got != "qmd" {
		t.Errorf("resolveQMDBinaryWith = %q, want \"qmd\" when nothing is installed", got)
	}
}

// qmd's shebang is `#!/usr/bin/env node`, so resolving qmd's absolute path
// is not sufficient — node must be resolvable too, and it lives in the
// same bin directory.
func TestQMDEnvPrependsBinaryDirToPATH(t *testing.T) {
	env := qmdEnvWith("/opt/node/v26/bin/qmd", []string{"HOME=/h", "PATH=/usr/bin:/bin"}, nil)
	var path string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if want := "/opt/node/v26/bin" + string(os.PathListSeparator) + "/usr/bin:/bin"; path != want {
		t.Errorf("PATH = %q, want %q", path, want)
	}
	if len(env) != 2 {
		t.Errorf("env grew to %d entries, want 2 (PATH replaced, not appended)", len(env))
	}
}

func TestQMDEnvAddsPATHWhenAbsentAndSkipsRelativeBinary(t *testing.T) {
	env := qmdEnvWith("/opt/node/bin/qmd", []string{"HOME=/h"}, nil)
	if len(env) != 2 || !strings.HasPrefix(env[1], "PATH=/opt/node/bin") {
		t.Errorf("env = %v, want a PATH entry appended", env)
	}

	// Unresolved bare name: filepath.Dir("qmd") is ".", and putting "." on
	// PATH is a footgun, so the env must be handed back untouched.
	in := []string{"PATH=/usr/bin"}
	if got := qmdEnvWith("qmd", in, nil); got[0] != "PATH=/usr/bin" || len(got) != 1 {
		t.Errorf("qmdEnv with relative binary mutated env: %v", got)
	}
}

func TestReachableFromCron(t *testing.T) {
	sep := string(os.PathListSeparator)
	cron := "/usr/bin" + sep + "/Users/x/.local/bin" + sep + "/opt/homebrew/bin"

	if !reachableFromCron("/Users/x/.local/bin/claude", cron) {
		t.Error("claude in ~/.local/bin should be reachable from cron")
	}
	// The actual bug: an nvm shim is on the interactive PATH but not cron's.
	if reachableFromCron("/Users/x/.nvm/versions/node/v26.4.0/bin/qmd", cron) {
		t.Error("an nvm-only binary must NOT be reported reachable from cron")
	}
	// Trailing-slash and dot forms are the same directory.
	if !reachableFromCron("/usr/bin/git", "/usr/bin/"+sep+"/tmp") {
		t.Error("trailing slash in cron PATH should still match")
	}
}

// A relative qmd_path is rejected: exec resolves it against cmd.Dir (the
// KB root) while isExecutableFile checks it against the cwd, so the file
// checked and the file run could differ.
func TestResolveQMDBinaryRejectsRelativeExplicitPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeExec(t, filepath.Join(dir, "qmd-rel"))
	t.Chdir(dir)
	if got := resolveQMDBinaryOnPath("qmd-rel", ""); got == "qmd-rel" || got == filepath.Join(dir, "qmd-rel") {
		t.Errorf("relative qmd_path was honored: %q", got)
	}
}

// doctor resolves against cron's PATH: a qmd only the interactive PATH can
// see must not resolve to an absolute path there.
func TestResolveQMDBinaryOnPathIgnoresProcessPATH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skipIfSystemQMD(t)
	interactiveDir := t.TempDir()
	want := writeExec(t, filepath.Join(interactiveDir, "qmd"))
	t.Setenv("PATH", interactiveDir)

	if got := resolveQMDBinaryOnPath("", interactiveDir); got != want {
		t.Errorf("on interactive PATH: got %q, want %q", got, want)
	}
	cronPath := t.TempDir()
	if got := resolveQMDBinaryOnPath("", cronPath); filepath.IsAbs(got) {
		t.Errorf("on cron PATH: got %q, want the bare-name fallback — the process PATH leaked in", got)
	}
}

func TestCheckQMDDepFailsWhenOnlyInteractivePATHHasQMD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skipIfSystemQMD(t)
	interactiveDir := t.TempDir()
	qmd := writeExec(t, filepath.Join(interactiveDir, "qmd"))
	t.Setenv("PATH", interactiveDir)

	got := checkQMDDep(depSpec{Name: "qmd", Binary: "qmd"}, "", t.TempDir(), true)
	if got.Status != statusFail {
		t.Fatalf("status = %v (%s), want FAIL: scheduled runs cannot find qmd", got.Status, got.Detail)
	}
	if !strings.Contains(got.Fix, qmd) || !strings.Contains(got.Fix, "scribe.local.yaml") {
		t.Errorf("fix %q should name the interactive path and scribe.local.yaml", got.Fix)
	}

	got = checkQMDDep(depSpec{Name: "qmd", Binary: "qmd"}, "", interactiveDir, true)
	if got.Status != statusOK {
		t.Errorf("status = %v (%s), want OK when cron's PATH has qmd", got.Status, got.Detail)
	}
}

// skipIfSystemQMD skips tests that expect qmd NOT to resolve: fixedQMDPaths
// also probes absolute system locations HOME does not isolate, so on a
// machine with qmd installed there the not-found path is unreachable,
// which says nothing about the code. Call after setting HOME.
func skipIfSystemQMD(t *testing.T) {
	t.Helper()
	for _, p := range fixedQMDPaths(os.Getenv("HOME")) {
		if isExecutableFile(p) {
			t.Skipf("qmd installed at %s on this machine", p)
		}
	}
}

// The bun case: qmd installed with `bun add -g` puts only the package shim
// in ~/.bun/bin, while bun itself can live under mise. The launcher execs
// bun, so the shim's own directory is not enough — the runtime's has to be
// appended too, or every cron reindex dies with `exec: bun: not found`.
func TestQMDRuntimeDirsFindsBunOutsideTheShimDir(t *testing.T) {
	home := t.TempDir()
	writeExec(t, filepath.Join(home, ".bun", "bin", "qmd"))
	bun := writeExec(t, filepath.Join(home, ".local", "share", "mise", "installs", "bun", "1.4.0", "bin", "bun"))
	writeExec(t, filepath.Join(home, ".local", "share", "mise", "installs", "bun", "1.1.0", "bin", "bun"))

	cronPath := filepath.Join(home, ".bun", "bin") + string(os.PathListSeparator) + t.TempDir()
	got := qmdRuntimeDirs(home, cronPath)
	if !slices.Contains(got, filepath.Dir(bun)) {
		t.Errorf("qmdRuntimeDirs = %v, want it to include the newest mise bun dir %s", got, filepath.Dir(bun))
	}
}

// A runtime the PATH already resolves is never overridden: the probe only
// fills gaps, it must not swap the user's node for another version.
func TestQMDRuntimeDirsLeavesResolvableRuntimeAlone(t *testing.T) {
	home := t.TempDir()
	onPath := t.TempDir()
	writeExec(t, filepath.Join(onPath, "node"))
	writeExec(t, filepath.Join(onPath, "bun"))
	writeExec(t, filepath.Join(home, ".nvm", "versions", "node", "v26.4.0", "bin", "node"))

	if got := qmdRuntimeDirs(home, onPath); len(got) != 0 {
		t.Errorf("qmdRuntimeDirs = %v, want none when PATH already has node and bun", got)
	}
}

// Probed runtime dirs go AFTER the caller's PATH.
func TestQMDEnvAppendsRuntimeDirsAfterPATH(t *testing.T) {
	probe := func(home, _ string) []string {
		if home != "/h" {
			t.Errorf("probe got home %q, want the env's HOME", home)
		}
		return []string{"/rt/bin"}
	}
	env := qmdEnvWith("/q/bin/qmd", []string{"HOME=/h", "PATH=/usr/bin"}, probe)
	sep := string(os.PathListSeparator)
	if want := "PATH=/q/bin" + sep + "/usr/bin" + sep + "/rt/bin"; env[1] != want {
		t.Errorf("env = %v, want %s", env, want)
	}
}

// Resolving is not running: a qmd that cron can find but not execute (its
// runtime is missing from cron's env) must FAIL, not report ok.
func TestCheckQMDDepFailsWhenQMDCannotRunUnderCron(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cronDir := t.TempDir()
	qmd := writeExec(t, filepath.Join(cronDir, "qmd"))

	orig := qmdCronProbe
	t.Cleanup(func() { qmdCronProbe = orig })
	qmdCronProbe = func(bin, _ string) error {
		if bin != qmd {
			t.Errorf("probe ran %q, want the resolved %q", bin, qmd)
		}
		return errors.New("exit status 127: exec: bun: not found")
	}

	got := checkQMDDep(depSpec{Name: "qmd", Binary: "qmd"}, "", cronDir, true)
	if got.Status != statusFail || !strings.Contains(got.Detail, "bun: not found") {
		t.Errorf("check = %+v, want FAIL carrying the runtime error", got)
	}
}
