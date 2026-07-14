package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	cmds := []string{"gen-cron", "gen-systemd", "scrape", "serve"}
	if len(args) == 0 || !slices.Contains(cmds, args[0]) {
		return fmt.Errorf("usage: cragcast <%s> [flags]", strings.Join(cmds, "|"))
	}
	cmd, rest := args[0], args[1:]

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yml")
	var region *string
	if cmd == "scrape" {
		region = fs.String("region", "", "limit to a single region id")
	}
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

	// Anchor config-relative paths (the regions dir, the data dir, and the
	// config path baked into generated units) to the config file's own
	// directory, so every command works regardless of the working directory it
	// is launched from — cron and systemd set none.
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
		return gen.RunCron(gen.CronOptions{ConfigPath: absConfig, Bin: cfg.Bin, User: cfg.User, Regions: regions})
	case "gen-systemd":
		return gen.RunSystemd(gen.SystemdOptions{ConfigPath: absConfig, Bin: cfg.Bin, User: cfg.User})
	case "scrape":
		regionIds := cfg.Regions
		if *region != "" {
			regionIds = []string{*region}
		}
		regions, err := loadRegions(configDir, regionIds)
		if err != nil {
			return err
		}
		return scrape.Run(scrape.ScrapeOptions{Regions: regions, DataDir: dataDir, UserAgent: cfg.UserAgent, Github: cfg.Github})
	case "serve":
		regions, err := loadRegions(configDir, cfg.Regions)
		if err != nil {
			return err
		}
		return serve.Run(serve.ServeOptions{Regions: regions, DataDir: dataDir, Addr: cfg.Addr, Github: cfg.Github})
	}
	return nil
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
