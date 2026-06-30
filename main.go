package main

import (
	"flag"
	"fmt"
	"os"
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

	switch cmd {
	case "gen-cron":
		return gen.RunCron(gen.CronOptions{ConfigPath: *configPath, Regions: cfg.Regions})
	case "gen-systemd":
		return gen.RunSystemd(gen.SystemdOptions{ConfigPath: *configPath})
	case "scrape":
		regions := cfg.Regions
		if *region != "" {
			r, ok := findRegion(cfg.Regions, *region)
			if !ok {
				return fmt.Errorf("region not found: %s", *region)
			}
			regions = []config.Region{r}
		}
		return scrape.Run(scrape.ScrapeOptions{Regions: regions, DataDir: "./data"})
	case "serve":
		ids := make([]string, len(cfg.Regions))
		for i, r := range cfg.Regions {
			ids[i] = r.Id
		}
		return serve.Run(serve.ServeOptions{Regions: ids, DataDir: "./data", Addr: ":8090"})
	}
	return nil
}

func findRegion(regions []config.Region, id string) (config.Region, bool) {
	for _, r := range regions {
		if r.Id == id {
			return r, true
		}
	}
	return config.Region{}, false
}
