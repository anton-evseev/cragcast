package scrape

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
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
// windows) with its wetness status, day-high, wind, and AQI for the region page.
type cragDay struct {
	Verdict    Verdict
	Reason     string
	Temp       string
	Wetness    string
	Highlights string
	AQI        string
	Wind       string
	CloudMean  float64 // mean cloud cover across valid morning/afternoon windows
	HighF      float64 // numeric day-high, for averaging the area temperature
	Valid      bool    // false when the day had no data
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	view := web.RegionView{Chrome: chrome}
	cragsData := map[string][]any{} // slug -> per-day [label, [[name,class,reason,temp],...]]

	total := 0 // crags in the region, for the scrape progress log
	for _, a := range r.Areas {
		total += len(a.Crags)
	}
	fetched, err := s.fetchRegion(ctx, r, loc)
	if err != nil {
		return err
	}
	// Check every location before writing pages so incomplete weather cannot
	// replace the previous forecast.
	for point, hours := range fetched {
		if err := validateWarmup(hours, now); err != nil {
			return fmt.Errorf("incomplete weather coverage for %g, %g: %w", point.Lat, point.Lon, err)
		}
		for _, day := range Aggregate(hours, loc, now, forecastDays) {
			if !day.AM.Valid || !day.PM.Valid {
				return fmt.Errorf("incomplete weather coverage for %g, %g on %s", point.Lat, point.Lon, day.Date.Format("2006-01-02"))
			}
		}
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
			hours := fetched[Coordinate{c.Lat, c.Lon}]
			prof := Profile{
				Aspect: c.Aspect, DryingFactor: DryoutFor(c.Rock), Seepage: c.Seepage,
			}
			fc := Aggregate(modelWetness(hours, prof), loc, now, forecastDays)
			if dates == nil {
				for _, d := range fc {
					dates = append(dates, d.Date)
				}
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
			var highSum, cloudSum float64
			var highN int
			for c := range perCrag {
				cd := perCrag[c][i]
				verdicts[c] = cd.Verdict
				if cd.Verdict != Green {
					cragIcons[c] = iconOf(Result{Verdict: cd.Verdict, Reason: cd.Reason}, cd.CloudMean)
				}
				conds[c] = [8]string{area.Crags[c].Name, classOf(cd.Verdict), cd.Reason, cd.Temp, cd.Wetness, cd.Wind, cd.AQI, cd.Highlights}
				if cd.Valid { // average only the crags that returned data
					highSum += cd.HighF
					cloudSum += cd.CloudMean
					highN++
				}
			}
			av := AreaVerdict(verdicts)
			temp := "—"
			cloudMean := 0.0
			if highN > 0 {
				cloudMean = cloudSum / float64(highN)
			}
			label := fmt.Sprintf("%s: %s", dates[i].Format("Mon"), verdictWord(av))
			if av == Green {
				_, sky := skyCondition(cloudMean)
				label += ", " + sky
			}
			if highN > 0 {
				temp = fmt.Sprintf("%.0f°", highSum/float64(highN))
				label += ", " + temp
			}
			summary.Cells = append(summary.Cells, web.SummaryCell{
				Class: classOf(av),
				Icons: areaIcon(av, cragIcons, cloudMean),
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
			AMData: areaWindowData(d.AM, p, "AM", th),
			PMData: areaWindowData(d.PM, p, "PM", th),
		}
		if am.Verdict == pm.Verdict {
			cell.Class = classOf(am.Verdict)
			limiting := worseResult(am, pm)
			cell.Icon = iconOf(limiting, dayCloudCover(d))
			cell.Temp = "—"
			if d.AM.Valid || d.PM.Valid {
				cell.Temp = fmt.Sprintf("%.0f°", max(d.AM.TmaxF, d.PM.TmaxF))
			}
			cell.Label = fmt.Sprintf("%s, %s", d.Date.Format("Mon"), conditionText(limiting, dayCloudCover(d)))
		} else {
			cell.Split = true
			cell.AM = web.Half{Class: classOf(am.Verdict), Icon: iconOf(am, d.AM.CloudMean)}
			cell.PM = web.Half{Class: classOf(pm.Verdict), Icon: iconOf(pm, d.PM.CloudMean)}
			cell.Label = fmt.Sprintf("%s, AM %s, PM %s", d.Date.Format("Mon"), conditionText(am, d.AM.CloudMean), conditionText(pm, d.PM.CloudMean))
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
		am := Explain(d.AM, p, "AM", th)
		pm := Explain(d.PM, p, "PM", th)
		limiting := worseResult(am.Result, pm.Result)
		grades := metricGrades(d.AM, p, "AM", th)
		pmGrades := metricGrades(d.PM, p, "PM", th)
		wetness := worseWetness(explainedWetness(d.AM, am.Result, grades), explainedWetness(d.PM, pm.Result, pmGrades))
		cd := cragDay{CloudMean: dayCloudCover(d), Highlights: dayHighlights(am.Result, pm.Result), Verdict: limiting.Verdict, Reason: limiting.Reason, Temp: "—", Wetness: wetness.Reason, Wind: "—", AQI: formatAQI(peakAQI(d.AM.AQI, d.PM.AQI))}
		if d.AM.Valid || d.PM.Valid {
			cd.Valid = true
			cd.HighF = max(d.AM.TmaxF, d.PM.TmaxF)
			cd.Temp = fmt.Sprintf("%.0f°", cd.HighF)
			if limiting.Reason == "cold" || limiting.Reason == "too cold" {
				// The low is what made it cold; the high would contradict the warning.
				low := math.Inf(1)
				for _, w := range []Window{d.AM, d.PM} {
					if w.Valid {
						low = min(low, w.TminF)
					}
				}
				cd.Temp = fmt.Sprintf("%.0f°", low)
			}
			cd.Wind = fmt.Sprintf("%.0f mph", max(d.AM.WindMaxMph, d.PM.WindMaxMph))
		}
		out = append(out, cd)
	}
	return out
}

// windowData packs one window into the compact attribute string the client
// formats: "class,reason,temp,wetness,wind,aqi,highlights,precip,chance,past,humidity,dew,wetnessGrades".
// Numbers are pre-rounded
// (the client only appends units); a no-data window carries empty stat fields.
// Reasons never contain commas, so a plain comma join is unambiguous.
func windowData(w Window, r Result, grades [6]Verdict) string {
	cls := classOf(r.Verdict)
	if !w.Valid {
		return strings.Join(append([]string{cls, r.Reason}, make([]string, 11)...), ",")
	}
	aq := ""
	if w.AQI != nil {
		aq = fmt.Sprintf("%.0f", *w.AQI)
	}
	wetness := explainedWetness(w, r, grades)
	chance, humidity := "", ""
	if w.PrecipProbability != nil {
		chance = fmt.Sprintf("%.0f", *w.PrecipProbability)
	}
	if w.HumidityMax != nil {
		humidity = fmt.Sprintf("%.0f", *w.HumidityMax)
	}
	wetnessGrades := strings.Join([]string{classOf(grades[1]), classOf(grades[2]), classOf(grades[3])}, "|")
	return fmt.Sprintf("%s,%s,%.0f,%s,%.0f,%s,%s,%.3f,%s,%.3f,%s,%.0f,%s",
		cls, r.Reason, w.TmaxF, wetness.Reason, w.WindMaxMph, aq, detailHighlights(grades, wetness),
		w.PrecipIn, chance, w.Antecedent, humidity, w.DewMaxF, wetnessGrades)
}

// areaWindowData appends the area-details fields to windowData: the
// temperature range, the displayed precipitation, highlights for only the
// measurements that explain the rating, and the rating's explanation.
func areaWindowData(w Window, p Profile, win string, th Thresholds) string {
	e := Explain(w, p, win, th)
	data := windowData(w, e.Result, metricGrades(w, p, win, th))
	if !w.Valid {
		return data + ",," + explanationSummary(e) + ",,,,,"
	}
	fields := strings.Split(data, ",")
	fields[2] = temperatureRange(w, e)
	fields[7] = formatPrecip(w.PrecipIn)
	cls := classOf(e.Verdict)
	rows := strings.Split("go|go|go|go|go|go|go", "|") // temp, precip, chance, past, dew, wind, aqi
	switch reasonMetric(e.Reason) {
	case MetricTemp:
		rows[0] = cls
	case MetricRain:
		if fields[7] != "0 in" || w.PrecipProbability == nil {
			rows[1] = cls
		}
		if w.PrecipProbability != nil {
			rows[2] = cls
		}
	case MetricDew:
		rows[4] = cls
	case MetricWind:
		rows[5] = cls
	case MetricAQI:
		rows[6] = cls
	}
	// Preserve indices 0..16; historical rain and RH are neutral context.
	surface, seep := moistureWindow(w, "surface"), moistureWindow(w, "seep")
	// Alternating conditions can make the combined window marginal even when
	// one component alone has four consecutive good hours. Show its actual
	// contribution rather than a contradictory "Dry likely" or "Low risk".
	if e.Verdict != Green {
		switch reasonMetric(e.Reason) {
		case MetricPastRain:
			if e.Verdict >= surface.Verdict {
				surface = e.Result
			}
		case MetricSeepage:
			if e.Verdict >= seep.Verdict {
				seep = e.Result
			}
		}
	}
	surfaceText, seepText := "Dry likely", "Low risk"
	if surface.Verdict != Green {
		surfaceText = strings.ToUpper(surface.Reason[:1]) + surface.Reason[1:]
	}
	if seep.Verdict != Green {
		seepText = strings.ToUpper(seep.Reason[:1]) + seep.Reason[1:]
	}
	if !p.Seepage {
		seepText = "Not expected"
	}
	return strings.Join(fields, ",") + "," + strings.Join(rows, "|") + "," + explanationSummary(e) +
		fmt.Sprintf(",%.3f,go|go,%s,%s,%s|%s", w.RecentPrecip, surfaceText, seepText,
			classOf(surface.Verdict), classOf(seep.Verdict))
}

func explainedWetness(w Window, r Result, grades [6]Verdict) Result {
	wetness := wetnessResult(w, grades)
	switch reasonMetric(r.Reason) {
	case MetricRain, MetricPastRain, MetricSeepage, MetricDew:
		if r.Verdict >= wetness.Verdict {
			return r
		}
	}
	return wetness
}

// temperatureRange shows the window's low–high, plus the sun-adjusted
// temperature when solar gain is what pushed it over a threshold.
func temperatureRange(w Window, e Explanation) string {
	text := fmt.Sprintf("%.0f–%.0f°F", w.TminF, w.TmaxF)
	if fmt.Sprintf("%.0f", w.TminF) == fmt.Sprintf("%.0f", w.TmaxF) {
		text = fmt.Sprintf("%.0f°F", w.TmaxF)
	}
	if e.FeelsF != nil && math.Abs(*e.FeelsF-e.TempF) >= 1 {
		text += fmt.Sprintf(" · feels %.0f°F in sun", *e.FeelsF)
	}
	return text
}

// formatPrecip hides model noise: amounts that round below 0.02 in read "trace".
func formatPrecip(in float64) string {
	switch {
	case in < 0.005:
		return "0 in"
	case in < 0.015:
		return "trace"
	default:
		return fmt.Sprintf("%.2f in", in)
	}
}

// dayHighlights marks the crag-list columns (temp, wetness, wind, AQI) that
// explain either window's rating, and nothing else.
func dayHighlights(results ...Result) string {
	var cols [4]Verdict
	for _, r := range results {
		if r.Verdict == Green {
			continue
		}
		col := -1
		switch reasonMetric(r.Reason) {
		case MetricTemp:
			col = 0
		case MetricRain, MetricPastRain, MetricSeepage, MetricDew:
			col = 1
		case MetricWind:
			col = 2
		case MetricAQI:
			col = 3
		}
		if col >= 0 {
			cols[col] = max(cols[col], r.Verdict)
		}
	}
	out := make([]string, len(cols))
	for i, v := range cols {
		out[i] = classOf(v)
	}
	return strings.Join(out, "|")
}

func detailHighlights(grades [6]Verdict, wetness Result) string {
	return strings.Join([]string{classOf(grades[0]), classOf(wetness.Verdict),
		classOf(grades[4]), classOf(grades[5])}, "|")
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
		return "poor"
	}
}

// areaIcon summarizes an area's day by merging its crags' individual icons
// (each from iconOf, excluding green crags). Green uses average cloud cover; a mix
// of a heat-limited crag (sun) and a wet one (rain cloud or drying drop) returns
// two icons — sun and the wetness glyph — which the template renders with a "/"
// between them; otherwise the single most-limiting icon shows.
func areaIcon(av Verdict, cragIcons []string, cloudCover float64) []string {
	if av == Green {
		icon, _ := skyCondition(cloudCover)
		return []string{icon}
	}
	var sun, rain, drop, wind, cold bool
	for _, ic := range cragIcons {
		if ic == "i-air" {
			return []string{"i-air"}
		}
	}
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
		case "i-cold":
			cold = true
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
	case cold:
		return []string{"i-cold"}
	default:
		return []string{"i-unknown"}
	}
}

func iconOf(r Result, cloudCover float64) string {
	switch {
	case r.Verdict == Green:
		icon, _ := skyCondition(cloudCover)
		return icon
	case r.Reason == "hot" || r.Reason == "too hot":
		return "i-sun" // heat is the culprit, whether Yellow or Red
	case r.Reason == "air quality":
		return "i-air"
	case r.Reason == "cold" || r.Reason == "too cold":
		return "i-cold"
	case r.Reason == "no data":
		return "i-unknown"
	case strings.HasPrefix(r.Reason, "rain "):
		return "i-rain"
	case r.Reason == "wet likely" || r.Reason == "drying" || strings.HasPrefix(r.Reason, "seepage "):
		return "i-drop"
	case r.Reason == "humid" || r.Reason == "very humid":
		return "i-drop"
	case r.Reason == "windy" || r.Reason == "too windy":
		return "i-wind"
	default:
		return "i-unknown"
	}
}

// dayCloudCover averages the two equal-length climbing windows, omitting missing data.
func dayCloudCover(d DayForecast) float64 {
	if !d.AM.Valid {
		return d.PM.CloudMean
	}
	if !d.PM.Valid {
		return d.AM.CloudMean
	}
	return (d.AM.CloudMean + d.PM.CloudMean) / 2
}

func skyCondition(cloudCover float64) (icon, description string) {
	switch {
	case cloudCover <= 20:
		return "i-sun", "mostly clear"
	case cloudCover < 80:
		return "i-partly-cloudy", "partly cloudy"
	default:
		return "i-cloud", "overcast"
	}
}

func conditionText(r Result, cloudCover float64) string {
	if r.Verdict == Green {
		_, sky := skyCondition(cloudCover)
		return conditionSummary(r) + ", " + sky
	}
	return conditionSummary(r)
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

func formatAQI(a *float64) string {
	if a == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f", *a)
}

// fetchRegion deduplicates exact coordinates and completes fetching before rendering.
func (s *scraper) fetchRegion(ctx context.Context, r config.Region, loc *time.Location) (map[Coordinate][]Hour, error) {
	points := []Coordinate{}
	seen := map[Coordinate]bool{}
	for _, a := range r.Areas {
		for _, c := range a.Crags {
			p := Coordinate{c.Lat, c.Lon}
			if !seen[p] {
				seen[p] = true
				points = append(points, p)
			}
		}
	}
	out := make(map[Coordinate][]Hour, len(points))
	for start := 0; start < len(points); start += batchSize {
		batch := points[start:min(start+batchSize, len(points))]
		log.Printf("Fetching weather and AQI for %d locations", len(batch))
		rows, err := s.fc.FetchBatch(ctx, batch, loc)
		if err != nil {
			return nil, fmt.Errorf("fetch batch: %w", err)
		}
		if len(rows) != len(batch) {
			return nil, fmt.Errorf("fetch batch returned %d locations, expected %d", len(rows), len(batch))
		}
		for i, p := range batch {
			out[p] = rows[i]
		}
	}
	return out, nil
}
