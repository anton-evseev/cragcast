// Package bundle prepares a complete scrape for static hosting.
package bundle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anton-evseev/cragcast/web"
)

type Options struct {
	Region string
	From   string
	Out    string
}

type asset struct {
	path string
	data []byte
}

// Run copies the scraped HTML and renders the static pages into Out.
func Run(o Options) error {
	regionID := o.Region
	regionPage, err := os.ReadFile(filepath.Join(o.From, regionID+".html"))
	if err != nil {
		return fmt.Errorf("read region forecast: %w", err)
	}
	assets := []asset{
		{path: "index.html", data: regionPage},
		{path: regionID + ".html", data: regionPage},
	}
	areaDir := filepath.Join(o.From, regionID)
	areas, err := os.ReadDir(areaDir)
	if err != nil {
		return fmt.Errorf("read area forecasts: %w", err)
	}
	for _, area := range areas {
		if area.IsDir() || filepath.Ext(area.Name()) != ".html" {
			continue
		}
		path := filepath.Join(regionID, area.Name())
		data, err := os.ReadFile(filepath.Join(o.From, path))
		if err != nil {
			return fmt.Errorf("read area forecast %q: %w", area.Name(), err)
		}
		assets = append(assets, asset{path: path, data: data})
	}
	var about, notFound bytes.Buffer
	if err := web.About(&about, web.AboutView{}); err != nil {
		return fmt.Errorf("render about page: %w", err)
	}
	if err := web.Error(&notFound, web.ErrorView{
		Status: 404, Message: "That page isn't available.",
		Footer: web.FooterData{ShowAbout: true},
	}); err != nil {
		return fmt.Errorf("render 404 page: %w", err)
	}
	assets = append(assets,
		asset{path: "about.html", data: about.Bytes()},
		asset{path: "404.html", data: notFound.Bytes()},
	)

	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(o.Out, regionID), 0o755); err != nil {
		return fmt.Errorf("create region directory: %w", err)
	}
	for _, a := range assets {
		if err := os.WriteFile(filepath.Join(o.Out, a.path), a.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", a.path, err)
		}
	}
	return nil
}
