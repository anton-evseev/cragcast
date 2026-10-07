package scrape

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/anton-evseev/cragcast/config"
	"github.com/anton-evseev/cragcast/web"
)

const forecastDays = 7

type ScrapeOptions struct {
	Regions []config.Region
	DataDir string
	Github  string
}

// cragDay is one crag's day collapsed to a single verdict (the worse of its two
// windows) with the numbers that explain it — day-high, in-window rain, 72h
// antecedent soak, plus peak dew point and wind. These are the same columns the
// crag subpage shows per window, so the region crag list reads as one with it.
type cragDay struct {
	Verdict Verdict
	Reason  string
	Temp    string
	Rain    string
	Soak    string
	Dew     string
	Wind    string
	HighF   float64 // numeric day-high, for averaging the area temperature
	Valid   bool    // false when the day had no data
}

// scraper holds the deployment-wide context for a run so it need not be threaded
// through every call: the forecaster, scoring thresholds, output dir, and the
// footer repo link.
type scraper struct {
	fc      Forecaster
	th      Thresholds
	dataDir string
	github  string
}

// Run scrapes each region (or one, if region != "") and writes the region
// summary page plus a subpage per area under data/<region>/.
func Run(o ScrapeOptions) error {
	s := &scraper{
		fc:      NewOpenMeteo(),
		th:      DefaultThresholds(),
		dataDir: o.DataDir,
		github:  o.Github,
	}
	for _, r := range o.Regions {
		if err := s.region(r); err != nil {
			return fmt.Errorf("region %s: %w", r.Name, err)
		}
	}
	return nil
}

func (s *scraper) region(r config.Region) error {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return fmt.Errorf("bad timezone %q: %w", r.Timezone, err)
	}
	now := time.Now().In(loc)
	chrome := web.Chrome{
		Region: r.Name, RegionId: r.Id,
		Footer:  web.FooterData{Github: s.github, ShowAbout: true},
		Updated: now.Format("Jan 2, 3:04 PM"), Days: dayHeaders(now, loc),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	view := web.RegionView{Chrome: chrome}
	cragsData := map[string][]any{} // slug -> per-day [label, [[name,class,reason,temp],...]]

	total := 0 // crags in the region, for the scrape progress log
	for _, a := range r.Areas {
		total += len(a.Crags)
	}
	scraped := 0

	for _, area := range r.Areas {
		if len(area.Crags) == 0 {
			continue // nothing to score — skip empty areas (no card, no subpage)
		}
		page := web.AreaView{Chrome: chrome, AreaName: area.Name, Drive: area.Drive}
		var dates []time.Time
		var perCrag [][]cragDay // [crag][day]

		for _, c := range area.Crags {
			scraped++
			log.Printf("[%d/%d] Scraping data for %s/%s (%g, %g)...", scraped, total, area.Name, c.Name, c.Lat, c.Lon)
			hours, err := s.fc.Fetch(ctx, c.Lat, c.Lon, loc)
			if err != nil {
				return fmt.Errorf("fetch %s: %w", c.Name, err)
			}
			fc := Aggregate(hours, loc, now, forecastDays)
			if dates == nil {
				for _, d := range fc {
					dates = append(dates, d.Date)
				}
			}
			prof := Profile{
				Aspect:       c.Aspect,
				DryoutInches: DryoutFor(c.Rock),
				Seepage:      c.Seepage,
			}
			page.Crags = append(page.Crags, web.CragView{
				Name:       c.Name,
				Aspect:     c.Aspect,
				AspectIcon: aspectIcon(c.Aspect),
				Cells:      cells(fc, prof, s.th),
			})
			perCrag = append(perCrag, cragDays(fc, prof, s.th))
		}

		summary := web.AreaSummary{Name: area.Name, Id: area.Id, Drive: area.Drive}
		dayEntries := make([]any, len(dates))
		for i := range dates {
			verdicts := make([]Verdict, len(perCrag))
			conds := make([][8]string, len(perCrag))
			cragIcons := make([]string, len(perCrag))
			var highSum float64
			var highN int
			for c := range perCrag {
				cd := perCrag[c][i]
				verdicts[c] = cd.Verdict
				cragIcons[c] = iconOf(Result{Verdict: cd.Verdict, Reason: cd.Reason})
				conds[c] = [8]string{area.Crags[c].Name, classOf(cd.Verdict), cd.Reason, cd.Temp, cd.Rain, cd.Soak, cd.Dew, cd.Wind}
				if cd.Valid { // average only the crags that returned data
					highSum += cd.HighF
					highN++
				}
			}
			av := AreaVerdict(verdicts)
			temp := "—"
			label := fmt.Sprintf("%s: %s", dates[i].Format("Mon"), verdictWord(av))
			if highN > 0 {
				temp = fmt.Sprintf("%.0f°", highSum/float64(highN))
				label += ", " + temp
			}
			summary.Cells = append(summary.Cells, web.SummaryCell{
				Class: classOf(av),
				Icons: areaIcon(av, cragIcons),
				Temp:  temp,
				Day:   i,
				Label: label,
			})
			dayEntries[i] = []any{dayLabel(dates[i], i), conds}
		}
		view.Areas = append(view.Areas, summary)
		cragsData[area.Id] = dayEntries

		var buf bytes.Buffer
		if err := web.Area(&buf, page); err != nil {
			return fmt.Errorf("render area %s: %w", area.Id, err)
		}
		if err := s.write(filepath.Join(r.Id, area.Id+".html"), buf.Bytes()); err != nil {
			return err
		}
	}

	blob, err := json.Marshal(cragsData)
	if err != nil {
		return fmt.Errorf("encode crag data: %w", err)
	}
	view.CragsJSON = template.JS(blob)
	var buf bytes.Buffer
	if err := web.Region(&buf, view); err != nil {
		return fmt.Errorf("render region: %w", err)
	}
	return s.write(r.Id+".html", buf.Bytes())
}

// write atomically saves data to dataDir/rel, creating parent dirs as needed.
func (s *scraper) write(rel string, data []byte) error {
	path := filepath.Join(s.dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, data)
}

// cells scores both windows per day and collapses to solid or split.
func cells(days []DayForecast, p Profile, th Thresholds) []web.Cell {
	out := make([]web.Cell, 0, len(days))
	for i, d := range days {
		am := Score(d.AM, p, "AM", th)
		pm := Score(d.PM, p, "PM", th)
		cell := web.Cell{
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
			cell.AM = web.Half{Class: classOf(am.Verdict), Icon: iconOf(am)}
			cell.PM = web.Half{Class: classOf(pm.Verdict), Icon: iconOf(pm)}
			cell.Label = fmt.Sprintf("%s, AM %s, PM %s", d.Date.Format("Mon"), am.Reason, pm.Reason)
		}
		out = append(out, cell)
	}
	return out
}

// cragDays collapses each day to the worse of its two windows — the limiting
// factor a climber cares about — for the area summary and crag list.
func cragDays(days []DayForecast, p Profile, th Thresholds) []cragDay {
	out := make([]cragDay, 0, len(days))
	for _, d := range days {
		am := Score(d.AM, p, "AM", th)
		pm := Score(d.PM, p, "PM", th)
		worse, reason := am.Verdict, am.Reason
		if pm.Verdict > am.Verdict {
			worse, reason = pm.Verdict, pm.Reason
		}
		cd := cragDay{Verdict: worse, Reason: reason, Temp: "—", Rain: "—", Soak: "—", Dew: "—", Wind: "—"}
		if d.AM.Valid || d.PM.Valid {
			cd.Valid = true
			cd.HighF = max(d.AM.TmaxF, d.PM.TmaxF)
			cd.Temp = fmt.Sprintf("%.0f°", cd.HighF)
			cd.Rain = fmt.Sprintf("%.2f″", d.AM.PrecipIn+d.PM.PrecipIn)
			cd.Soak = fmt.Sprintf("%.2f″", d.AM.Antecedent)
			cd.Dew = fmt.Sprintf("%.0f°", max(d.AM.DewMaxF, d.PM.DewMaxF))
			cd.Wind = fmt.Sprintf("%.0f mph", max(d.AM.WindMaxMph, d.PM.WindMaxMph))
		}
		out = append(out, cd)
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

func dayHeaders(now time.Time, loc *time.Location) []web.DayHeader {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	hs := make([]web.DayHeader, 0, forecastDays)
	for d := 0; d < forecastDays; d++ {
		date := today.AddDate(0, 0, d)
		label := date.Format("Mon")
		if d == 0 {
			label = "Today"
		}
		hs = append(hs, web.DayHeader{Label: label, Date: date.Format("Jan 2")})
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

func verdictWord(v Verdict) string {
	switch v {
	case Green:
		return "good"
	case Yellow:
		return "marginal"
	default:
		return "wet"
	}
}

// areaIcon summarizes an area's day by merging its crags' individual icons
// (each from iconOf, the single reason→icon mapping). Green wins outright; a mix
// of a heat-limited crag (sun) and a wet one (rain cloud or drying drop) returns
// two icons — sun and the wetness glyph — which the template renders with a "/"
// between them; otherwise the single most-limiting icon shows.
func areaIcon(av Verdict, cragIcons []string) []string {
	if av == Green {
		return []string{"i-check"}
	}
	var sun, rain, drop, wind bool
	for _, ic := range cragIcons {
		switch ic {
		case "i-sun":
			sun = true
		case "i-rain":
			rain = true
		case "i-drop":
			drop = true
		case "i-wind":
			wind = true
		}
	}
	wet := "i-drop"
	if rain {
		wet = "i-rain"
	}
	switch {
	case sun && (rain || drop):
		return []string{"i-sun", wet}
	case sun:
		return []string{"i-sun"}
	case rain:
		return []string{"i-rain"}
	case drop:
		return []string{"i-drop"}
	case wind:
		return []string{"i-wind"}
	default:
		return []string{"i-drop"}
	}
}

func iconOf(r Result) string {
	switch {
	case r.Verdict == Green:
		return "i-check"
	case r.Reason == "hot" || r.Reason == "too hot":
		return "i-sun" // heat is the culprit, whether Yellow or Red
	case r.Reason == "windy":
		return "i-wind"
	case r.Verdict == Red:
		return "i-rain"
	default: // Other Yellow reasons use the drop
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
