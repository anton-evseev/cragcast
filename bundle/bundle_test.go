package bundle

import (
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
