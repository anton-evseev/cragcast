package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScrapeWithoutDeploymentConfig(t *testing.T) {
	root := t.TempDir()
	regionsDir := filepath.Join(root, "regions")
	if err := os.Mkdir(regionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regionYAML := []byte("id: seattle\nname: Seattle\ntimezone: America/Los_Angeles\nareas:\n  - id: exits\n    name: Exits\n    drive: 45 min\n")
	if err := os.WriteFile(filepath.Join(regionsDir, "seattle.yml"), regionYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "forecast")
	if err := run([]string{"scrape", "--region", "seattle", "--regions-dir", regionsDir, "--out", out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "seattle.html")); err != nil {
		t.Fatal(err)
	}
}

func TestDeployCloudflareArguments(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	for _, args := range [][]string{
		{"deploy-cloudflare"},
		{"deploy-cloudflare", "--from", t.TempDir()},
		{"deploy-cloudflare", "--from", t.TempDir(), "--worker", "../bad"},
		{"deploy-cloudflare", "--from", t.TempDir(), "unexpected"},
		{"deploy-cloudflare", "--unknown"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
