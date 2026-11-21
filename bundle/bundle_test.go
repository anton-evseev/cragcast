package bundle

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "forecast")
	if err := os.MkdirAll(filepath.Join(from, "seattle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "seattle.html"), []byte("region forecast"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "dist")
	options := Options{Region: "seattle", From: from, Out: out}
	if err := Run(options); err != nil {
		t.Fatalf("bundle should accept an empty area directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(from, "seattle", "exits.html"), []byte("area forecast"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(options); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"index.html":         "region forecast",
		"seattle.html":       "region forecast",
		"seattle/exits.html": "area forecast",
	} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %q, err %v; want %q", name, got, err, want)
		}
	}
	for name, expected := range map[string]string{
		"about.html": "About — Cragcast",
		"404.html":   "That page isn't available.",
	} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || !strings.Contains(html.UnescapeString(string(got)), expected) {
			t.Errorf("%s: missing %q, err %v", name, expected, err)
		}
	}
}

func TestRunCheckSize(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "forecast")
	if err := os.MkdirAll(filepath.Join(from, "seattle"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(from, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("seattle.html", []byte("region forecast"))
	var report strings.Builder
	options := Options{Region: "seattle", From: from, Out: filepath.Join(root, "ok"), CheckSize: true, Report: &report}
	if err := Run(options); err != nil {
		t.Fatalf("small pages should pass: %v", err)
	}
	if !strings.Contains(report.String(), "4 files: 4 passed, 0 failed (100.0%)") {
		t.Errorf("unexpected report:\n%s", report.String())
	}

	big := make([]byte, MaxGzipBytes)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	write("seattle/huge.html", []byte(hex.EncodeToString(big)))
	options.Out = filepath.Join(root, "big")
	report.Reset()
	err := Run(options)
	if err == nil || !strings.Contains(report.String(), "seattle/huge.html") {
		t.Fatalf("want size error and report naming seattle/huge.html; got %v", err)
	}
	if !strings.Contains(report.String(), "FAIL") || !strings.Contains(report.String(), "5 files: 4 passed, 1 failed (80.0%)") {
		t.Errorf("unexpected report:\n%s", report.String())
	}
	options.CheckSize = false
	options.Out = filepath.Join(root, "unchecked")
	if err := Run(options); err != nil {
		t.Fatalf("size check should be opt-in: %v", err)
	}
}
