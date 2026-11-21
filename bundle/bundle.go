// Package bundle prepares a complete scrape for static hosting.
package bundle

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/anton-evseev/cragcast/web"
)

type Options struct {
	Region string
	From   string
	Out    string
	// CheckSize fails the bundle if any HTML page exceeds MaxGzipBytes gzipped.
	CheckSize bool
	// Report receives the size table; defaults to os.Stdout.
	Report io.Writer
}

// MaxGzipBytes is the gzipped size budget for each HTML page.
const MaxGzipBytes = 14 * 1024

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
	static, err := fs.ReadDir(web.Static, ".")
	if err != nil {
		return fmt.Errorf("read static files: %w", err)
	}
	for _, f := range static {
		data, err := fs.ReadFile(web.Static, f.Name())
		if err != nil {
			return fmt.Errorf("read static file %q: %w", f.Name(), err)
		}
		assets = append(assets, asset{path: f.Name(), data: data})
	}

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
	if o.CheckSize {
		report := o.Report
		if report == nil {
			report = os.Stdout
		}
		return checkSize(assets, report)
	}
	return nil
}

type countWriter struct{ n int }

func (c *countWriter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }

func kib(n int) string { return fmt.Sprintf("%.1f KiB", float64(n)/1024) }

// checkSize prints a size table for every HTML page and fails if any page's
// gzipped size exceeds MaxGzipBytes.
func checkSize(assets []asset, report io.Writer) error {
	var pages []asset
	for _, a := range assets {
		if filepath.Ext(a.path) == ".html" {
			pages = append(pages, a)
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].path < pages[j].path })
	tw := tabwriter.NewWriter(report, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tSIZE\tGZIP\tLIMIT\tRESULT")
	passed := 0
	for _, a := range pages {
		var c countWriter
		zw, _ := gzip.NewWriterLevel(&c, gzip.BestCompression)
		if _, err := zw.Write(a.data); err != nil {
			return fmt.Errorf("gzip %s: %w", a.path, err)
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("gzip %s: %w", a.path, err)
		}
		result := "FAIL"
		if c.n <= MaxGzipBytes {
			result = "PASS"
			passed++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.path, kib(len(a.data)), kib(c.n), kib(MaxGzipBytes), result)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	total := len(pages)
	pct := 100.0
	if total > 0 {
		pct = 100 * float64(passed) / float64(total)
	}
	fmt.Fprintf(report, "---\n%d files: %d passed, %d failed (%.1f%%)\n", total, passed, total-passed, pct)
	if passed < total {
		return fmt.Errorf("%d of %d pages over the %d-byte gzip limit", total-passed, total, MaxGzipBytes)
	}
	return nil
}
