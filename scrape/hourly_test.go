package scrape

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anton-evseev/cragcast/web"
)

// steadyWindow makes the constant conditions in legacy scoring fixtures
// explicit. Tests for variable weather use individual hourly observations.
func steadyWindow(w Window) Window {
	w.hours = nil
	if !w.Valid {
		return w
	}
	if w.TminF == 0 {
		w.TminF = w.TmaxF
	}
	start := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	for i := range 6 {
		w.hours = append(w.hours, Hour{Time: start.Add(time.Duration(i) * time.Hour),
			TempF: w.TmaxF, DewF: w.DewMaxF, WindMph: w.WindMaxMph,
			Cloud: w.CloudMean, AQI: w.AQI, Humidity: w.HumidityMax})
	}
	return w
}

// withWetness supplies explicit modeled evidence without inferring it from totals.
func withWetness(w Window, surface, seep float64) Window {
	w.hours = append([]Hour(nil), w.hours...)
	for i := range w.hours {
		w.hours[i].wetness = moisture{surface: surface, seep: seep}
	}
	return w
}

func TestHourlyDurationRules(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: 0.4}
	for _, tc := range []struct {
		pattern string
		want    Verdict
	}{
		{"GGGGGG", Green}, {"YYYYYY", Yellow}, {"RRRRRR", Red},
		{"GGGYYY", Yellow}, {"GGGGYY", Green}, {"YYGGGG", Green},
		{"GGYGGY", Yellow}, {"GGGGRR", Yellow}, {"RRRRYY", Red},
		{"RYRYRR", Red}, {"RRRYYY", Yellow}, {"GGGGGY", Green},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			w := steadyWindow(Window{Valid: true, TmaxF: 65})
			for i, grade := range tc.pattern {
				switch grade {
				case 'Y':
					w.hours[i].WindMph = 25
				case 'R':
					w.hours[i].WindMph = 35
				}
			}
			got := Score(w, p, "AM", DefaultThresholds())
			if got.Verdict != tc.want {
				t.Fatalf("got %+v, want %v", got, tc.want)
			}
		})
	}
}

func TestHourlySimultaneousConditions(t *testing.T) {
	p := Profile{Aspect: "sun all day", DryingFactor: 0.4}
	w := steadyWindow(Window{Valid: true, TmaxF: 79, CloudMean: 100})
	// Hotter hours are cloudy; clear hours are cooler. Combining the maximum
	// temperature with average cloud cover would incorrectly warn about heat.
	for i := 0; i < 3; i++ {
		w.hours[i].TempF = 65
		w.hours[i].Cloud = 0
	}
	w.CloudMean = 50
	if got := Score(w, p, "AM", DefaultThresholds()); got.Verdict != Green {
		t.Fatal(got)
	}
	// A warm window maximum must not hide four cold hours.
	for i := 0; i < 4; i++ {
		w.hours[i].TempF = 20
		w.hours[i].Cloud = 100
	}
	if got := Score(w, p, "AM", DefaultThresholds()); got != (Result{Red, "too cold"}) {
		t.Fatal(got)
	}
}

func TestHourlyWetnessFloor(t *testing.T) {
	th := DefaultThresholds()
	for _, tc := range []struct {
		name    string
		amount  float64
		chance  *float64
		past    float64
		rock    string
		seepage bool
		want    Result
	}{
		{"amount only", .02, nil, 0, "granite", false, Result{Red, "rain forecast"}},
		{"likely", .02, value(60), 0, "granite", false, Result{Red, "rain likely"}},
		{"possible", .02, value(20), 0, "granite", false, Result{Yellow, "rain possible"}},
		{"low probability", .02, value(5), 0, "granite", false, Result{Green, "good"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: tc.amount,
				PrecipProbability: tc.chance, Antecedent: tc.past})
			p := Profile{Aspect: "shade", DryingFactor: DryoutFor(tc.rock), Seepage: tc.seepage}
			if got := Score(w, p, "AM", th); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHourlyMissingCoverage(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: 0.4}
	for _, kind := range []string{"no samples", "five samples", "duplicate", "gap"} {
		t.Run(kind, func(t *testing.T) {
			w := steadyWindow(Window{Valid: true, TmaxF: 65})
			switch kind {
			case "no samples":
				w.hours = nil
			case "five samples":
				w.hours = w.hours[:5]
			case "duplicate":
				w.hours[3].Time = w.hours[2].Time
			case "gap":
				w.hours[5].Time = w.hours[5].Time.Add(time.Hour)
			}
			if got := Score(w, p, "AM", DefaultThresholds()); got != (Result{Yellow, "no data"}) {
				t.Fatal(got)
			}
		})
	}
}

func TestHourlyReasonTies(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []Result
		want Result
	}{
		{"frequency", []Result{{Red, "air quality"}, {Yellow, "windy"}, {Yellow, "windy"}}, Result{Yellow, "windy"}},
		{"severity", []Result{{Yellow, "hot"}, {Red, "too windy"}}, Result{Red, "too windy"}},
		{"priority", []Result{{Red, "too hot"}, {Red, "air quality"}}, Result{Red, "air quality"}},
		{"earliest", []Result{{Red, "too cold"}, {Red, "too hot"}}, Result{Red, "too cold"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dominantReason(tc.in); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHourlyConditionsPayload(t *testing.T) {
	w := steadyWindow(Window{Valid: true, TmaxF: 65})
	w.hours[5].WindMph = 35
	w.WindMaxMph = 35
	p := Profile{Aspect: "shade", DryingFactor: .4}
	th := DefaultThresholds()
	cell := cells([]DayForecast{{AM: w, PM: w}}, p, th)[0]
	fields := strings.Split(cell.AMData, ",")
	if len(fields) != 20 || fields[14] != "Marginal · too windy after 12pm" || fields[4] != "35" {
		t.Fatal(fields)
	}
	if !strings.Contains(cell.Label, "Marginal · too windy") || cell.Class != "warn" || cell.Icon != "i-wind" {
		t.Fatal(cell)
	}
	var html strings.Builder
	if err := web.Area(&html, web.AreaView{Crags: []web.CragView{{Cells: []web.Cell{cell}}}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Conditions", "Marginal · too windy", "p[14]", "Surface wetness and seepage are estimates", "AM 7am–1pm", "Past precipitation (24h)", "Past precipitation (72h)", "p[15]", "p[19]", "Surface wetness", "Seepage"} {
		if !strings.Contains(html.String(), text) {
			t.Fatalf("missing %q", text)
		}
	}
	missing := strings.Split(areaWindowData(Window{}, p, "AM", th), ",")
	if len(missing) != 20 || missing[14] != "No data" {
		t.Fatal(missing)
	}
}

func TestHourlyOptionalMetrics(t *testing.T) {
	w := steadyWindow(Window{Valid: true, TmaxF: 65})
	p := Profile{Aspect: "shade", DryingFactor: .4}
	th := DefaultThresholds()
	// Missing optional metrics do not invalidate complete required weather.
	if got := Score(w, p, "AM", th); got.Verdict != Green {
		t.Fatal(got)
	}
	// Available AQI still participates hour by hour; one Poor hour prevents Good.
	w.hours[5].AQI = value(150)
	if got := Score(w, p, "AM", th); got != (Result{Yellow, "air quality"}) {
		t.Fatal(got)
	}
	for i := range 4 {
		w.hours[i].AQI = value(150)
	}
	if got := Score(w, p, "AM", th); got != (Result{Red, "air quality"}) {
		t.Fatal(got)
	}
}

func TestHourlyAggregationBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{
		time.Date(2026, 3, 8, 0, 0, 0, 0, loc),
		time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
	} {
		t.Run(date.Format("2006-01-02"), func(t *testing.T) {
			start := time.Date(date.Year(), date.Month(), date.Day(), 7, 0, 0, 0, loc)
			end := time.Date(date.Year(), date.Month(), date.Day(), 19, 0, 0, 0, loc)
			var hours []Hour
			for ts := start.Add(-71 * time.Hour); !ts.After(end); ts = ts.Add(time.Hour) {
				h := Hour{Time: ts, TempF: 65}
				if ts.Hour() == 13 || ts.Hour() == 19 {
					h.WindMph = 35
				}
				hours = append(hours, h)
			}
			// Aggregation sorts samples; continuity must not depend on input order.
			slices.Reverse(hours)
			day := Aggregate(hours, loc, date, 1)[0]
			for _, w := range []Window{day.AM, day.PM} {
				if !w.Valid || len(w.hours) != 6 {
					t.Fatalf("invalid window: %+v", w)
				}
			}
			if day.AM.hours[0].Time.Hour() != 7 || day.AM.hours[5].Time.Hour() != 12 ||
				day.PM.hours[0].Time.Hour() != 13 || day.PM.hours[5].Time.Hour() != 18 {
				t.Fatal("incorrect local window boundaries")
			}
			p := Profile{Aspect: "shade", DryingFactor: .4}
			th := DefaultThresholds()
			if Score(day.AM, p, "AM", th).Verdict != Green || Score(day.PM, p, "PM", th).Verdict != Yellow {
				t.Fatal("boundary weather assigned to wrong window")
			}
			cell := cells([]DayForecast{day}, p, th)[0]
			if !cell.Split || cell.AM.Class != "go" || cell.PM.Class != "warn" {
				t.Fatal(cell)
			}
			if daily := cragDays([]DayForecast{day}, p, th)[0]; daily.Verdict != Yellow {
				t.Fatal("daily rating must remain the worse half")
			}
		})
	}
}

// These fixtures record intended changes against the old peak-based verdict.
func TestHourlyComparedWithPeaks(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: .4}
	th := DefaultThresholds()
	for _, tc := range []struct {
		name       string
		windHours  int
		wind       float64
		rain, past float64
		old, new   Verdict
	}{
		{"brief discomfort", 1, 25, 0, 0, Yellow, Green},
		{"brief poor weather", 1, 35, 0, 0, Red, Yellow},
		{"persistent poor weather", 4, 35, 0, 0, Red, Red},
		{"mixed window", 3, 25, 0, 0, Yellow, Yellow},
		{"rain unchanged", 0, 0, .02, 0, Red, Red},
		{"old rain totals have no floor", 0, 0, 0, .4, Green, Green},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: tc.wind,
				PrecipIn: tc.rain, Antecedent: tc.past})
			for i := range w.hours {
				w.hours[i].WindMph = 0
				if i < tc.windHours {
					w.hours[i].WindMph = tc.wind
				}
			}
			old := Green
			for _, grade := range metricGrades(w, p, "AM", th) {
				old = max(old, grade)
			}
			if got := Score(w, p, "AM", th); old != tc.old || got.Verdict != tc.new {
				t.Fatalf("old=%v new=%+v; want old=%v new=%v", old, got, tc.old, tc.new)
			}
		})
	}
}

func TestHourlyConditionsCopy(t *testing.T) {
	for _, tc := range []struct {
		result Result
		text   string
	}{
		{Result{Green, "good"}, "Good"},
		{Result{Yellow, "windy"}, "Marginal · windy"},
		{Result{Yellow, "rain possible"}, "Marginal · rain possible"},
		{Result{Yellow, "drying"}, "Marginal · drying"},
		{Result{Red, "wet likely"}, "Poor · wet likely"},
		{Result{Red, "too windy"}, "Poor · too windy"},
		{Result{Yellow, "no data"}, "No data"},
	} {
		if got := conditionSummary(tc.result); got != tc.text {
			t.Fatalf("got %q, want %q", got, tc.text)
		}
	}
	var html strings.Builder
	if err := web.About(&html, web.AboutView{}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"four consecutive good hours", "at least four poor hours", "uncalibrated estimates", "worse half"} {
		if !strings.Contains(html.String(), text) {
			t.Fatalf("missing About explanation %q", text)
		}
	}
}
