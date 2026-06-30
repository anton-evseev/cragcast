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

	// Tap-to-expand detail. Carried as compact data-* attributes and formatted
	// client-side (see the script in forecast.gohtml). Each window string is
	// "class,reason,temp,precip,soak,dew,wind"; stats are empty when no data.
	Day    string // panel header, e.g. "Wed, Jun 18"
	AMData string
	PMData string
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
	for i, d := range days {
		am := Score(d.AM, p, "AM", th)
		pm := Score(d.PM, p, "PM", th)
		cell := Cell{
			Day:    dayLabel(d.Date, i),
			AMData: windowData(d.AM, am),
			PMData: windowData(d.PM, pm),
		}
		if am.Verdict == pm.Verdict {
			cell.Class = classOf(am.Verdict)
			cell.Icon = iconOf(am)
			cell.Temp = fmt.Sprintf("%.0f°", max(d.AM.TmaxF, d.PM.TmaxF))
			cell.Label = fmt.Sprintf("%s, %s", d.Date.Format("Mon"), am.Reason)
		} else {
			cell.Split = true
			cell.AM = Half{Class: classOf(am.Verdict), Icon: iconOf(am)}
			cell.PM = Half{Class: classOf(pm.Verdict), Icon: iconOf(pm)}
			cell.Label = fmt.Sprintf("%s, AM %s, PM %s", d.Date.Format("Mon"), am.Reason, pm.Reason)
		}
		out = append(out, cell)
	}
	return out
}

// windowData packs one window into the compact attribute string the client
// formats: "class,reason,temp,precip,soak,dew,wind". Numbers are pre-rounded
// (the client only appends units); a no-data window carries empty stat fields.
// Reasons never contain commas, so a plain comma join is unambiguous.
func windowData(w Window, r Result) string {
	cls := classOf(r.Verdict)
	if !w.Valid {
		return cls + "," + r.Reason + ",,,,,"
	}
	return fmt.Sprintf("%s,%s,%.0f,%.2f,%.2f,%.0f,%.0f",
		cls, r.Reason, w.TmaxF, w.PrecipIn, w.Antecedent, w.DewMaxF, w.WindMaxMph)
}

// dayLabel is the detail-panel header, e.g. "Today, Jun 18" or "Wed, Jun 18".
func dayLabel(date time.Time, i int) string {
	if i == 0 {
		return "Today, " + date.Format("Jan 2")
	}
	return date.Format("Mon, Jan 2")
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
	switch {
	case r.Verdict == Green:
		return "i-check"
	case r.Reason == "hot" || r.Reason == "too hot":
		return "i-sun" // heat is the culprit, whether Yellow or Red
	case r.Verdict == Red:
		return "i-rain"
	default: // Yellow, non-heat — reason-bearing drop
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
