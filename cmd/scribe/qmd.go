package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// qmd is distributed as an npm package, which puts it wherever the user's
// node version manager keeps its shims — for nvm that is
// ~/.nvm/versions/node/<version>/bin, a directory added to PATH by
// ~/.zshrc. A LaunchAgent runs `/bin/zsh -lc`, which is NOT interactive
// and therefore never sources ~/.zshrc, so `qmd` was simply absent from
// PATH for every scheduled run: every cron reindex was a silent no-op
// while `qmd` resolved fine in an interactive shell. The login shell's
// PATH is launchd's /usr/bin:/bin:/usr/sbin:/sbin plus whatever
// /etc/paths, /etc/paths.d and ~/.zprofile add — typically Homebrew's
// prefix, and not ~/.local/bin or any version manager.
//
// Two things are therefore required to invoke qmd from cron, and an
// absolute path alone is not enough:
//
//  1. locate the binary without relying on PATH (resolveQMDBinary), and
//  2. make its JS runtime resolvable (runQMD → qmdEnv). The npm shim is a
//     `#!/usr/bin/env node` script, and the 2.x launcher is a /bin/sh
//     wrapper that execs `node` or `bun` depending on which package
//     manager installed it. Under nvm, fnm, volta and asdf node ships in
//     the same bin directory as the shim, so prepending that directory is
//     enough. A bun global install is not: ~/.bun/bin holds only the
//     package shims, and bun itself may live under mise or Homebrew. So
//     qmdEnv also appends the directory of any runtime it can find that
//     the PATH does not already resolve.
//
// nodeManagerQMDGlobs are the per-user install roots to probe, in
// preference order. Kept to the managers that actually colocate node and
// the package shim; a manager that does not is a bug report, not a guess.
func nodeManagerQMDGlobs(home string) []string {
	return []string{
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "qmd"),
		filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin", "qmd"),
		filepath.Join(home, ".volta", "bin", "qmd"),
		filepath.Join(home, ".bun", "bin", "qmd"),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", "qmd"),
		filepath.Join(home, ".local", "share", "mise", "installs", "node", "*", "bin", "qmd"),
	}
}

// fixedQMDPaths are non-manager locations, probed after the managers.
func fixedQMDPaths(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin", "qmd"),
		"/opt/homebrew/bin/qmd",
		"/usr/local/bin/qmd",
		"/usr/bin/qmd",
	}
}

// resolveQMDBinary returns the qmd binary to exec. Order:
//
//  1. cfg.QMDPath   — explicit escape hatch for an unusual install
//  2. PATH          — correct for an interactive run, and for cron when
//     qmd lives somewhere path_helper already covers
//  3. probe         — the node-manager and fixed locations above
//
// Falls back to the bare name "qmd" when nothing is found, so the caller
// produces the same "executable file not found" error it always did
// rather than a confusing empty-path exec failure.
func resolveQMDBinary(root string) string {
	var explicit string
	if cfg := loadConfig(root); cfg != nil {
		explicit = cfg.QMDPath
	}
	return resolveQMDBinaryWith(explicit)
}

// resolveQMDBinaryWith is resolveQMDBinary for callers that already hold
// the config, resolving against this process's own PATH.
func resolveQMDBinaryWith(explicit string) string {
	return resolveQMDBinaryOnPath(explicit, os.Getenv("PATH"))
}

// resolveQMDBinaryOnPath resolves against an explicit PATH list. doctor
// passes cron's PATH here: resolving against its own interactive PATH
// would report a qmd that only the interactive shell can see as fine.
//
// An explicit qmd_path must be absolute. exec resolves a relative path
// against cmd.Dir (the KB root), not the cwd isExecutableFile checks, so
// the file checked and the file run could differ.
func resolveQMDBinaryOnPath(explicit, pathList string) string {
	if explicit != "" {
		if p := expandHome(explicit); filepath.IsAbs(p) && isExecutableFile(p) {
			return p
		}
	}
	if p := lookPathIn("qmd", pathList); p != "" {
		return p
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "qmd"
	}
	for _, pattern := range nodeManagerQMDGlobs(home) {
		if p := newestExecutableMatch(pattern); p != "" {
			return p
		}
	}
	for _, p := range fixedQMDPaths(home) {
		if isExecutableFile(p) {
			return p
		}
	}
	return "qmd"
}

// lookPathIn is exec.LookPath against a caller-supplied PATH list. Empty
// and relative entries are skipped, so a "." on PATH cannot resolve.
func lookPathIn(name, pathList string) string {
	for dir := range strings.SplitSeq(pathList, string(os.PathListSeparator)) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, name); isExecutableFile(p) {
			return p
		}
	}
	return ""
}

// newestExecutableMatch expands a glob and returns the executable match
// with the highest embedded version number. A machine that has held
// several node releases keeps all of them under nvm, and only the
// version qmd was `npm i -g`'d into has the shim — filtering on
// executability usually leaves exactly one. When it leaves several,
// newest wins, which matches what an interactive shell would have used.
func newestExecutableMatch(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return ""
	}
	var found []string
	for _, m := range matches {
		if isExecutableFile(m) {
			found = append(found, m)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		return compareVersionPaths(found[i], found[j]) > 0
	})
	return found[0]
}

// compareVersionPaths orders two paths by the first dotted numeric run in
// each, so v26.4.0 sorts above v9.0.0 (a plain string sort gets this
// backwards). Returns >0 when a is newer.
func compareVersionPaths(a, b string) int {
	av, bv := versionNumbers(a), versionNumbers(b)
	for i := 0; i < len(av) || i < len(bv); i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			return x - y
		}
	}
	return strings.Compare(a, b)
}

// versionNumbers pulls the longest dotted numeric sequence out of a path
// (".../node/v26.4.0/bin/qmd" -> [26 4 0]).
func versionNumbers(path string) []int {
	var best []int
	for seg := range strings.SplitSeq(path, string(os.PathSeparator)) {
		seg = strings.TrimPrefix(seg, "v")
		parts := strings.Split(seg, ".")
		nums := make([]int, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				nums = nil
				break
			}
			nums = append(nums, n)
		}
		if len(nums) > len(best) {
			best = nums
		}
	}
	return best
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

// runQMD execs qmd with its own directory prepended to PATH so the
// `#!/usr/bin/env node` shebang resolves. Returns trimmed combined
// output plus the error, matching runCmdErr's contract.
func runQMD(root string, args ...string) (string, error) {
	bin := resolveQMDBinary(root)
	cmd := exec.Command(bin, args...) //nolint:noctx // sync wrapper, mirrors runCmdErr
	if root != "" {
		cmd.Dir = root
	}
	cmd.Env = qmdEnv(bin, os.Environ())
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// qmdEnv returns env with the binary's directory prepended to PATH, and
// the directories of any JS runtime (node, bun) that PATH still cannot
// resolve appended after it. Appended, not prepended, so a runtime the
// caller's PATH already provides keeps winning.
func qmdEnv(bin string, env []string) []string {
	return qmdEnvWith(bin, env, qmdRuntimeDirs)
}

// qmdEnvWith is qmdEnv with the runtime prober injected, so tests do not
// depend on which node or bun the machine running them has installed.
//
// The bin-dir prepend is only applied for an absolute resolution:
// prepending filepath.Dir("qmd") would put "." on PATH, which is both
// useless and a footgun. An unresolved bare name gets the env back
// untouched — exec will fail with "not found" either way.
func qmdEnvWith(bin string, env []string, runtimes func(home, pathList string) []string) []string {
	if !filepath.IsAbs(bin) {
		return env
	}
	sep := string(os.PathListSeparator)
	home := os.Getenv("HOME")
	path := ""
	pathIdx := -1
	for i, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PATH="):
			path, pathIdx = strings.TrimPrefix(kv, "PATH="), i
		case strings.HasPrefix(kv, "HOME="):
			home = strings.TrimPrefix(kv, "HOME=")
		}
	}
	newPath := filepath.Dir(bin)
	if path != "" {
		newPath += sep + path
	}
	if runtimes != nil {
		for _, dir := range runtimes(home, newPath) {
			newPath += sep + dir
		}
	}
	out := make([]string, 0, len(env)+1)
	out = append(out, env...)
	if pathIdx >= 0 {
		out[pathIdx] = "PATH=" + newPath
	} else {
		out = append(out, "PATH="+newPath)
	}
	return out
}

// qmdRuntimes are the interpreters a qmd launcher may exec.
var qmdRuntimes = []string{"node", "bun"}

// runtimeCandidates lists where a version manager or package manager puts
// the named runtime, globs first, in the same preference order as
// nodeManagerQMDGlobs.
func runtimeCandidates(home, name string) []string {
	var c []string
	switch name {
	case "node":
		c = append(c,
			filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node"),
			filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin", "node"),
			filepath.Join(home, ".volta", "bin", "node"),
			filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", "node"),
		)
	case "bun":
		c = append(c,
			filepath.Join(home, ".bun", "bin", "bun"),
			filepath.Join(home, ".asdf", "installs", "bun", "*", "bin", "bun"),
		)
	}
	return append(c,
		filepath.Join(home, ".local", "share", "mise", "installs", name, "*", "bin", name),
		"/opt/homebrew/bin/"+name,
		"/usr/local/bin/"+name,
	)
}

// qmdRuntimeDirs returns, for each runtime in qmdRuntimes that pathList
// cannot resolve, the directory of the first install the probe finds.
// A runtime the PATH already resolves is left alone, so this only ever
// fills a gap — it never overrides the user's choice of node.
func qmdRuntimeDirs(home, pathList string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, name := range qmdRuntimes {
		if lookPathIn(name, pathList) != "" {
			continue
		}
		for _, pattern := range runtimeCandidates(home, name) {
			// An unset HOME turns the per-user candidates relative,
			// which would resolve against the cwd.
			if !filepath.IsAbs(pattern) {
				continue
			}
			if p := newestExecutableMatch(pattern); p != "" {
				if dir := filepath.Dir(p); !seen[dir] {
					seen[dir] = true
					dirs = append(dirs, dir)
				}
				break
			}
		}
	}
	return dirs
}
