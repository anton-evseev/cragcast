package scrape

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/anton-evseev/cragcast/config"
)

//go:embed forecast.gohtml
var tmplSrc string
var tmpl = template.Must(template.New("forecast").Parse(tmplSrc))

const forecastDays = 7

type ScrapeOptions struct {
	Regions []config.Region
	DataDir string
}

// ----- view model the template ranges over -----

type ForecastView struct {
	Region  string
	Updated string
	Days    []DayHeader
	Areas   []AreaView
}
type DayHeader struct {
	Label string // "Today", "Wed", ...
	Date  string // "Jun 16"
}
type AreaView struct {
	Name  string
	Drive string
	Crags []CragView
}
type CragView struct {
	Name       string
	Aspect     string
	AspectIcon string
	Cells      []Cell // exactly forecastDays
}

// Cell is either a solid verdict or a split AM/PM pair (template branches on Split).
type Cell struct {
	Split bool
	Class string // solid: go|warn|wet
	Icon  string // solid icon id
	Temp  string // solid day-high, e.g. "58°"
	AM    Half
	PM    Half
	Label string // aria-label
}
type Half struct {
	Class string
	Icon  string
}

// Run scrapes each region (or one, if region != "") and writes data/<region>.html.
func Run(o ScrapeOptions) error {
	f := NewOpenMeteo()
	th := DefaultThresholds()

	for _, r := range o.Regions {
		if err := scrapeRegion(f, th, r, o.DataDir); err != nil {
			return fmt.Errorf("region %s: %w", r.Name, err)
		}
	}
	return nil
}

func scrapeRegion(f Forecaster, th Thresholds, r config.Region, dataDir string) error {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return fmt.Errorf("bad timezone %q: %w", r.Timezone, err)
	}
	now := time.Now().In(loc)

	view := ForecastView{
		Region:  r.Name,
		Updated: now.Format("Mon, 3:04 PM"),
		Days:    dayHeaders(now, loc),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, area := range r.Areas {
		av := AreaView{Name: area.Name, Drive: area.Drive}
		for _, c := range area.Crags {
			hours, err := f.Fetch(ctx, c.Lat, c.Lon, loc)
			if err != nil {
				return fmt.Errorf("fetch %s: %w", c.Name, err)
			}
			days := Aggregate(hours, loc, now, forecastDays)
			prof := Profile{
				Aspect:       c.Aspect,
				DryoutInches: DryoutFor(c.Rock),
				Seepage:      c.Seepage,
			}
			av.Crags = append(av.Crags, CragView{
				Name:       c.Name,
				Aspect:     c.Aspect,
				AspectIcon: aspectIcon(c.Aspect),
				Cells:      cells(days, prof, th),
			})
		}
		view.Areas = append(view.Areas, av)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	return atomicWrite(filepath.Join(dataDir, r.Id+".html"), buf.Bytes())
}

// cells scores both windows per day and collapses to solid or split.
func cells(days []DayForecast, p Profile, th Thresholds) []Cell {
	out := make([]Cell, 0, len(days))
	for _, d := range days {
		am := Score(d.AM, p, "AM", th)
		pm := Score(d.PM, p, "PM", th)
		if am.Verdict == pm.Verdict {
			out = append(out, Cell{
				Class: classOf(am.Verdict),
				Icon:  iconOf(am),
				Temp:  fmt.Sprintf("%.0f°", max(d.AM.TmaxF, d.PM.TmaxF)),
				Label: fmt.Sprintf("%s, %s", d.Date.Format("Mon"), am.Reason),
			})
		} else {
			out = append(out, Cell{
				Split: true,
				AM:    Half{Class: classOf(am.Verdict), Icon: iconOf(am)},
				PM:    Half{Class: classOf(pm.Verdict), Icon: iconOf(pm)},
				Label: fmt.Sprintf("%s, AM %s, PM %s", d.Date.Format("Mon"), am.Reason, pm.Reason),
			})
		}
	}
	return out
}

func dayHeaders(now time.Time, loc *time.Location) []DayHeader {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	hs := make([]DayHeader, 0, forecastDays)
	for d := 0; d < forecastDays; d++ {
		date := today.AddDate(0, 0, d)
		label := date.Format("Mon")
		if d == 0 {
			label = "Today"
		}
		hs = append(hs, DayHeader{Label: label, Date: date.Format("Jan 2")})
	}
	return hs
}

func classOf(v Verdict) string {
	switch v {
	case Green:
		return "go"
	case Yellow:
		return "warn"
	default:
		return "wet"
	}
}

func iconOf(r Result) string {
	switch r.Verdict {
	case Green:
		return "i-check"
	case Red:
		return "i-rain"
	default: // Yellow — reason-bearing icon
		if r.Reason == "hot" {
			return "i-sun"
		}
		return "i-drop"
	}
}

func aspectIcon(aspect string) string {
	switch aspect {
	case "sun all day", "AM sun", "PM sun":
		return "i-sun"
	default:
		return "i-cloud"
	}
}

// atomicWrite writes via a temp file + rename so readers never see a partial
// file and a failed scrape leaves the previous good file untouched.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".forecast-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
