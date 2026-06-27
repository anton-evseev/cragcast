package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/anton-evseev/cragcast/config"
	"github.com/anton-evseev/cragcast/gen"
	"github.com/anton-evseev/cragcast/serve"
)

func main() {
	cmds := []string{"gen-cron", "gen-systemd", "scrape", "serve"}

	if len(os.Args) < 2 || !slices.Contains(cmds, os.Args[1]) {
		fmt.Fprintln(os.Stderr, "usage: cragcast <%s> [flags]", strings.Join(cmds, "|"))
		os.Exit(2)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", "", "TODO")
	if cmd == "scrape" {
		// TODO: region param
	}
	fs.Parse(os.Args[2:])

	if *configPath == "" {
		fmt.Fprintf(os.Stderr, "TODO")
		os.Exit(2)
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		// TODO
		os.Exit(2)
	}

	switch cmd {
	case "gen-cron":
		opts := gen.CronOptions{
			ConfigPath: *configPath,
			Regions:    cfg.Regions,
		}
		err := gen.RunCron(opts)
		if err != nil {
			// TODO
			os.Exit(2)
		}
	case "gen-systemd":
		opts := gen.SystemdOptions{
			ConfigPath: *configPath,
		}
		err := gen.RunSystemd(opts)
		if err != nil {
			// TODO
			os.Exit(2)
		}
	case "scrape":
		break
	case "serve":
		regions := make([]string, len(cfg.Regions))
		for i, r := range cfg.Regions {
			regions[i] = r.Name
		}
		opts := serve.ServeOptions{
			Regions: regions,
			DataDir: "/var/lib/cragcast",
			Addr:    ":8090",
		}
		err := serve.Run(opts)
		if err != nil {
			// TODO:
			os.Exit(2)
		}
	}
}
