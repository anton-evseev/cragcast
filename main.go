package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/anton-evseev/cragcast/bundle"
	"github.com/anton-evseev/cragcast/config"
	"github.com/anton-evseev/cragcast/deploycloudflare"
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
	cmds := []string{"bundle", "deploy-cloudflare", "scrape", "serve"}
	if len(args) == 0 {
		return fmt.Errorf("usage: cragcast <%s> [flags]", strings.Join(cmds, "|"))
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "bundle":
		return runBundle(rest)
	case "deploy-cloudflare":
		return runDeployCloudflare(rest)
	case "scrape":
		return runScrape(rest)
	case "serve":
		return runServe(rest)
	default:
		return fmt.Errorf("usage: cragcast <%s> [flags]", strings.Join(cmds, "|"))
	}
}

func runDeployCloudflare(args []string) error {
	fs := flag.NewFlagSet("deploy-cloudflare", flag.ContinueOnError)
	from := fs.String("from", "", "directory containing bundled static assets")
	worker := fs.String("worker", "cragcast", "Cloudflare Worker name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("deploy-cloudflare: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return deploycloudflare.Run(context.Background(), deploycloudflare.Options{
		From: *from, Worker: *worker,
		AccountID: os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		APIToken:  os.Getenv("CLOUDFLARE_API_TOKEN"),
	})
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
