package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/anton-evseev/cragcast/bundle"
	"github.com/anton-evseev/cragcast/config"
	"github.com/anton-evseev/cragcast/gen"
	"github.com/anton-evseev/cragcast/scrape"
	"github.com/anton-evseev/cragcast/serve"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cragcast:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmds := []string{"bundle", "gen-cron", "gen-systemd", "scrape", "serve"}
	if len(args) == 0 || !slices.Contains(cmds, args[0]) {
		return fmt.Errorf("usage: cragcast <%s> [flags]", strings.Join(cmds, "|"))
	}
	cmd, rest := args[0], args[1:]
	if cmd == "bundle" {
		return runBundle(rest)
	}
	if cmd == "scrape" {
		return runScrape(rest)
	}
	if cmd == "serve" {
		return runServe(rest)
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("%s: --config is required", cmd)
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	// Anchor paths used by generated cron and systemd units to the config file's
	// directory, since neither scheduler sets a working directory.
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	configDir := filepath.Dir(absConfig)
	dataDir := resolveDir(configDir, cfg.Data)

	switch cmd {
	case "gen-cron":
		regions, err := loadRegions(configDir, cfg.Regions)
		if err != nil {
			return err
		}
		return gen.RunCron(gen.CronOptions{
			Bin: cfg.Bin, User: cfg.User, Regions: regions,
			RegionsDir: filepath.Join(configDir, "regions"), Out: dataDir,
			Github: cfg.Github,
		})
	case "gen-systemd":
		return gen.RunSystemd(gen.SystemdOptions{
			Bin: cfg.Bin, User: cfg.User, Regions: cfg.Regions,
			RegionsDir: filepath.Join(configDir, "regions"), From: dataDir,
			Addr: cfg.Addr, Github: cfg.Github,
		})
	}
	return nil
}

func runScrape(args []string) error {
	fs := flag.NewFlagSet("scrape", flag.ContinueOnError)
	regionID := fs.String("region", "", "region id")
	regionsDir := fs.String("regions-dir", "regions", "directory containing regional YAML files")
	out := fs.String("out", "", "directory for scraped HTML")
	github := fs.String("github", "", "optional repository URL for page footers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("scrape: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *regionID == "" || strings.ContainsAny(*regionID, "/\\ \t") || *regionID == "." || *regionID == ".." {
		return fmt.Errorf("scrape: invalid region %q", *regionID)
	}
	if *out == "" {
		return fmt.Errorf("scrape: --out is required")
	}
	region, err := config.LoadRegionConfig(filepath.Join(*regionsDir, *regionID+".yml"))
	if err != nil {
		return err
	}
	if region.Id != *regionID {
		return fmt.Errorf("scrape: region id %q in config does not match %q", region.Id, *regionID)
	}
	return scrape.Run(scrape.ScrapeOptions{
		Regions: []config.Region{region}, DataDir: *out, Github: *github,
	})
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var regionIDs []string
	fs.Func("region", "region id (repeat for multiple regions)", func(id string) error {
		if id == "" || strings.ContainsAny(id, "/\\ \t") || id == "." || id == ".." {
			return fmt.Errorf("invalid region %q", id)
		}
		if slices.Contains(regionIDs, id) {
			return fmt.Errorf("duplicate region %q", id)
		}
		regionIDs = append(regionIDs, id)
		return nil
	})
	regionsDir := fs.String("regions-dir", "regions", "directory containing regional YAML files")
	from := fs.String("from", "", "directory containing scraped HTML")
	addr := fs.String("addr", ":8090", "address to listen on")
	github := fs.String("github", "", "optional repository URL for page footers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("serve: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if len(regionIDs) == 0 || *from == "" {
		return fmt.Errorf("serve: --region and --from are required")
	}
	regions := make([]config.Region, len(regionIDs))
	for i, id := range regionIDs {
		region, err := config.LoadRegionConfig(filepath.Join(*regionsDir, id+".yml"))
		if err != nil {
			return err
		}
		if region.Id != id {
			return fmt.Errorf("serve: region id %q in config does not match %q", region.Id, id)
		}
		regions[i] = region
	}
	return serve.Run(serve.ServeOptions{Regions: regions, DataDir: *from, Addr: *addr, Github: *github})
}

func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	regionId := fs.String("region", "", "region id")
	from := fs.String("from", "", "directory containing scraped HTML")
	out := fs.String("out", "", "new directory for static assets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("bundle: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *regionId == "" || strings.ContainsAny(*regionId, "/\\ \t") || *regionId == "." || *regionId == ".." {
		return fmt.Errorf("bundle: invalid region %q", *regionId)
	}
	if *from == "" || *out == "" {
		return fmt.Errorf("bundle: --from and --out are required")
	}
	return bundle.Run(bundle.Options{Region: *regionId, From: *from, Out: *out})
}

func loadRegions(configDir string, regionIds []string) ([]config.Region, error) {
	regions := make([]config.Region, len(regionIds))
	for i, id := range regionIds {
		path := filepath.Join(configDir, "regions", id+".yml")
		r, err := config.LoadRegionConfig(path)
		if err != nil {
			return nil, err
		}
		regions[i] = r
	}
	return regions, nil
}

// resolveDir makes a config-relative path absolute by anchoring it to the
// config file's directory; an already-absolute path is returned unchanged.
func resolveDir(configDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(configDir, p)
}
