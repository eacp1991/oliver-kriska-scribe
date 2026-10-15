package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 3A of the 2026-10-15 audit plan: repo-controlled names and CLI
// inputs that become path segments.

func TestSafePathSegment(t *testing.T) {
	for _, ok := range []string{"general", "my-project", "a.b", "Spaced Name", "ünïcode"} {
		if !safePathSegment(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "/abs", "x\x00y"} {
		if safePathSegment(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestConfigDropsNamesThatAreNotSegments(t *testing.T) {
	root := setupTrustKB(t,
		"kb_name: \"../docs\"\ndomains: [\"ok\", \"../../outside\"]\nmeta:\n  rolling_targets: [\"learnings\", \"../../../tmp/x\"]\n", "")
	cfg := loadConfig(root)
	if cfg.KBName != "" || kbName(root) != filepath.Base(root) {
		t.Errorf("kb_name with a separator must fall back to the directory name, got %q / %q", cfg.KBName, kbName(root))
	}
	if strings.Join(cfg.Domains, ",") != "ok" {
		t.Errorf("domains = %v, want only the plain name", cfg.Domains)
	}
	if strings.Join(cfg.Meta.RollingTargets, ",") != "learnings" {
		t.Errorf("rolling_targets = %v, want only the plain name", cfg.Meta.RollingTargets)
	}
	if allowedDomainsForRoot(root)["../../outside"] {
		t.Error("traversal domain reached the rolling-append allowlist")
	}
}

func TestWriteRollingRefusesProjectEscape(t *testing.T) {
	root := testKBRoot(t)
	w := &WriteCmd{Rolling: "learnings", Project: "../../outside", Title: "x"}
	err := w.runRolling(root, "body")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected containment error, got %v", err)
	}
	w = &WriteCmd{Rolling: "learnings", Project: "..", Title: "x"}
	if err := w.runRolling(root, "body"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected containment error for '..', got %v", err)
	}
}

func TestValidateDropNames(t *testing.T) {
	if err := validateDropNames("my-note", "scriptorium"); err != nil {
		t.Fatalf("plain names refused: %v", err)
	}
	if err := validateDropNames("../../note", "kb"); err == nil {
		t.Fatal("slug traversal accepted")
	}
	if err := validateDropNames("note", "../other"); err == nil {
		t.Fatal("kb-name traversal accepted")
	}
}

func TestMarkerImagesStayInsideOutDir(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "marker-out")
	kb := filepath.Join(base, "kb")
	for _, d := range []string{out, kb} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// One legitimate image inside the marker output, one reference that
	// points above it.
	if err := os.WriteFile(filepath.Join(out, "fig1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.png"), []byte("leak"), 0o644); err != nil {
		t.Fatal(err)
	}
	md := "![a](fig1.png)\n![b](../secret.png)\n"
	preserveMarkerImages(md, out, kb, "doc")
	if _, err := os.Stat(filepath.Join(kb, "raw", "assets", "doc", "fig1.png")); err != nil {
		t.Errorf("legitimate image not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(kb, "raw", "assets", "doc", "secret.png")); err == nil {
		t.Error("image reference above the marker output dir was copied into the KB")
	}
}
