package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/anton-evseev/cragcast/config"
	"github.com/anton-evseev/cragcast/web"
)

func value(v float64) *float64 { return &v }

func TestAQIGrades(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 1}
	for _, tc := range []struct {
		aqi    *float64
		grade  Verdict
		reason string
	}{
		{nil, Green, "good"}, {value(0), Green, "good"}, {value(50), Green, "good"},
		{value(50.1), Green, "good"}, {value(99.9), Green, "good"},
		{value(100), Yellow, "air quality"},
		{value(100.1), Yellow, "air quality"}, {value(101), Yellow, "air quality"},
		{value(149), Yellow, "air quality"}, {value(149.9), Yellow, "air quality"},
		{value(150), Red, "air quality"}, {value(151), Red, "air quality"},
	} {
		w := steadyWindow(Window{Valid: true, TmaxF: 65, AQI: tc.aqi})
		got := Score(w, p, "AM", DefaultThresholds())
		if got.Verdict != tc.grade || got.Reason != tc.reason {
			t.Fatalf("AQI %v: %+v", tc.aqi, got)
		}
	}
	w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 1, AQI: value(75)})
	if got := Score(w, p, "AM", DefaultThresholds()); got.Verdict != Red || got.Reason != "rain forecast" {
		t.Fatal(got)
	}
	w.AQI = value(150)
	w = steadyWindow(w)
	// Window-level rain wins a severity tie under the new conservative floor.
	if got := Score(w, p, "AM", DefaultThresholds()); got.Reason != "rain forecast" {
		t.Fatal(got)
	}
	w.PrecipIn = 0
	if got := Score(w, p, "AM", DefaultThresholds()); got.Reason != "air quality" {
		t.Fatal(got)
	}
}

func TestAQIAggregationAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{time.Date(2026, 3, 8, 0, 0, 0, 0, loc), time.Date(2026, 11, 1, 0, 0, 0, 0, loc)} {
		var hours []Hour
		for h := 6; h < 20; h++ {
			point := Hour{Time: time.Date(date.Year(), date.Month(), date.Day(), h, 0, 0, 0, loc), TempF: 65}
			if h == 7 {
				point.AQI = value(0)
			}
			if h == 12 {
				point.AQI = value(120)
			}
			if h == 13 {
				point.AQI = value(70)
			}
			if h == 19 {
				point.AQI = value(500)
			}
			hours = append(hours, point)
		}
		days := Aggregate(hours, loc, date, 2)
		if *days[0].AM.AQI != 120 || *days[0].PM.AQI != 70 || days[1].AM.AQI != nil {
			t.Fatal(days)
		}
	}
}

func weatherRow(ts []int64, temp float64) map[string]any {
	vals := make([]float64, len(ts))
	zeros := make([]float64, len(ts))
	for i := range vals {
		vals[i] = temp
	}
	return map[string]any{"hourly": map[string]any{"time": ts, "temperature_2m": vals, "precipitation": zeros, "wind_speed_10m": zeros, "dew_point_2m": zeros, "cloud_cover": zeros}}
}

func TestBatchJoinAndFailure(t *testing.T) {
	for _, mode := range []string{"ok", "http", "malformed", "count", "ragged", "api-error", "weather-failure"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var active, peak atomic.Int32
				var weatherCalls, airCalls atomic.Int32
				weatherEntered := make(chan struct{}, 1)
				airEntered := make(chan struct{}, 1)
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
					}
					calls := &weatherCalls
					if r.URL.Path == "/air" {
						calls = &airCalls
					}
					send, receive := weatherEntered, airEntered
					if r.URL.Path == "/air" {
						send, receive = airEntered, weatherEntered
					}
					if calls.Add(1) == 1 {
						send <- struct{}{}
						select {
						case <-receive:
						case <-time.After(time.Second):
							t.Error("requests did not overlap")
						}
					}
					if r.Header.Get("User-Agent") != userAgent || r.URL.Query().Get("timeformat") != "unixtime" {
						t.Error("request settings")
					}
					count := len(strings.Split(r.URL.Query().Get("latitude"), ","))
					rows := make([]any, count)
					if r.URL.Path == "/air" {
						switch mode {
						case "http":
							w.WriteHeader(503)
							return
						case "malformed":
							fmt.Fprint(w, "invalid")
							return
						case "count":
							fmt.Fprint(w, "[]")
							return
						}
						for i := range rows {
							vals := []any{nil, float64(80 + i)}
							if mode == "ragged" {
								vals = nil
							}
							rows[i] = map[string]any{"hourly": map[string]any{"time": []int64{7200, 3600}, "us_aqi": vals}}
							if mode == "api-error" {
								rows[i] = map[string]any{"error": true, "reason": "bad"}
							}
						}
					} else {
						if mode == "weather-failure" {
							w.WriteHeader(503)
							return
						}
						for i := range rows {
							rows[i] = weatherRow([]int64{0, 3600, 7200}, float64(60+i))
						}
					}
					if count == 1 {
						json.NewEncoder(w).Encode(rows[0])
					} else {
						json.NewEncoder(w).Encode(rows)
					}
				}))
				o := &OpenMeteo{Client: server.Client(), BaseURL: server.URL + "/weather", AirQualityURL: server.URL + "/air"}
				for _, count := range []int{1, 2} {
					weatherCalls.Store(0)
					airCalls.Store(0)
					points := make([]Coordinate, count)
					rows, err := o.FetchBatch(context.Background(), points, time.UTC)
					if mode == "weather-failure" {
						if err == nil {
							t.Fatal("expected weather error")
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					for i, row := range rows {
						if row[0].AQI != nil || row[2].AQI != nil {
							t.Fatal("null/missing became AQI")
						}
						if mode == "ok" {
							if row[1].AQI == nil || *row[1].AQI != float64(80+i) || row[1].TempF != float64(60+i) {
								t.Fatal(row)
							}
						} else if row[1].AQI != nil {
							t.Fatal("failed AQI retained")
						}
					}
				}
				if peak.Load() != 2 {
					t.Fatalf("peak requests=%d", peak.Load())
				}
			})
		})
	}
}

type batchFake struct{ sizes []int }

func (f *batchFake) FetchBatch(_ context.Context, p []Coordinate, _ *time.Location) ([][]Hour, error) {
	f.sizes = append(f.sizes, len(p))
	out := make([][]Hour, len(p))
	for i := range p {
		out[i] = []Hour{{TempF: p[i].Lat}}
	}
	return out, nil
}
func TestDedupAndBatchBoundary(t *testing.T) {
	f := &batchFake{}
	s := scraper{fc: f}
	r := config.Region{Areas: []config.Area{{}}}
	for i := 0; i < 33; i++ {
		r.Areas[0].Crags = append(r.Areas[0].Crags, config.Crag{Lat: float64(i), Lon: 1})
	}
	r.Areas[0].Crags = append(r.Areas[0].Crags, r.Areas[0].Crags[0])
	got, err := s.fetchRegion(context.Background(), r, time.UTC)
	if err != nil || len(got) != 33 || fmt.Sprint(f.sizes) != "[32 1]" {
		t.Fatalf("%v %v %d", err, f.sizes, len(got))
	}
}

func TestAQIRenderingAndTies(t *testing.T) {
	day := DayForecast{Date: time.Now(), AM: steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 30}), PM: steadyWindow(Window{Valid: true, TmaxF: 65, AQI: value(125)})}
	p := Profile{Aspect: "shade", DryoutInches: 1}
	c := cells([]DayForecast{day}, p, DefaultThresholds())[0]
	if c.Icon != "i-air" || !strings.Contains(c.PMData, ",125") {
		t.Fatal(c)
	}
	cd := cragDays([]DayForecast{day}, p, DefaultThresholds())[0]
	if cd.Reason != "air quality" || cd.AQI != "125" {
		t.Fatal(cd)
	}
	if got := areaIcon(Yellow, []string{"i-rain", "i-air"}, 0); got[0] != "i-air" {
		t.Fatal(got)
	}
	var region, area strings.Builder
	if err := web.Region(&region, web.RegionView{CragsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := web.Area(&area, web.AreaView{Crags: []web.CragView{{Cells: []web.Cell{c}}}}); err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct {
		html  string
		label string
	}{{region.String(), ">AQI</span>"}, {area.String(), "Air Quality Index"}} {
		html := page.html
		if !strings.Contains(html, page.label) || !strings.Contains(html, "CAMS") || !strings.Contains(html, "i-air") {
			t.Fatal("missing AQI UI")
		}
	}
}

func TestIndependentMetricWarnings(t *testing.T) {
	// Rain is red while AQI 101 is independently yellow.
	w := steadyWindow(Window{Valid: true, TmaxF: 51, PrecipIn: 0.09, Antecedent: 0.17, AQI: value(101)})
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	r := Score(w, p, "AM", th)
	if r.Verdict != Red || r.Reason != "rain forecast" {
		t.Fatal(r)
	}
	grades := metricGrades(w, p, "AM", th)
	if grades[1] != Red || grades[5] != Yellow {
		t.Fatal(grades)
	}
	data := strings.Split(windowData(w, r, grades), ",")
	if len(data) != 13 || data[6] != "go|wet|go|warn" {
		t.Fatal(data)
	}
	day := DayForecast{AM: w, PM: steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 30})}
	cd := cragDays([]DayForecast{day}, p, th)[0]
	// Only measurements behind a rating are highlighted: AM rain, PM wind.
	if cd.Highlights != "go|wet|warn|go" {
		t.Fatal(cd.Highlights)
	}
	w.AQI = nil
	if metricGrades(w, p, "AM", th)[5] != Green {
		t.Fatal("missing AQI highlighted")
	}
	w.AQI = value(150)
	if metricGrades(w, p, "AM", th)[5] != Red {
		t.Fatal("red AQI not highlighted")
	}
}

func TestDailySoakMatchesHighlight(t *testing.T) {
	// The daily wetness status must retain PM's drying warning.
	day := DayForecast{
		AM: steadyWindow(Window{Valid: true, TmaxF: 65}),
		PM: steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.29}),
	}
	for _, tc := range []struct {
		dryout float64
		class  string
	}{{0.4, "warn"}, {0.2, "wet"}} {
		p := Profile{Aspect: "shade", DryoutInches: tc.dryout}
		cd := cragDays([]DayForecast{day}, p, DefaultThresholds())[0]
		if strings.Split(cd.Highlights, "|")[1] != tc.class ||
			(tc.class == "warn" && cd.Wetness != "drying") ||
			(tc.class == "wet" && cd.Wetness != "wet rock") {
			t.Fatalf("wetness=%s highlights=%s", cd.Wetness, cd.Highlights)
		}
	}
}

func TestMetricBands(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	cases := []struct {
		name   string
		window Window
		grade  Verdict
		metric int
		reason string
	}{
		{"dry", steadyWindow(Window{Valid: true, TmaxF: 65}), Green, 1, "good"},
		{"trace rain", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.004}), Green, 1, "good"},
		{"below yellow rain", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.0099}), Green, 1, "good"},
		{"yellow rain boundary", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.01}), Yellow, 1, "rain possible"},
		{"Icicle Creek rain", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.016}), Yellow, 1, "rain possible"},
		{"rain boundary", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.02}), Red, 1, "rain forecast"},
		{"Tumwater rain", steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.032}), Red, 1, "rain forecast"},
		{"wind green", steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 24.9}), Green, 4, "good"},
		{"wind yellow", steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 25}), Yellow, 4, "windy"},
		{"wind below red", steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 34.9}), Yellow, 4, "windy"},
		{"wind red", steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 35}), Red, 4, "too windy"},
		{"dew green", steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 59.9}), Green, 3, "good"},
		{"dew yellow", steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 60}), Yellow, 3, "humid"},
		{"dew below red", steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 69.9}), Yellow, 3, "humid"},
		{"dew red", steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 70}), Red, 3, "very humid"},
		{"soak green", steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.199}), Green, 2, "good"},
		{"soak yellow", steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.2}), Yellow, 2, "drying"},
		{"soak red", steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.4}), Red, 2, "wet rock"},
		{"cold red", steadyWindow(Window{Valid: true, TmaxF: 28}), Red, 0, "too cold"},
		{"cold yellow", steadyWindow(Window{Valid: true, TmaxF: 36}), Yellow, 0, "cold"},
		{"temp green", steadyWindow(Window{Valid: true, TmaxF: 65}), Green, 0, "good"},
		{"hot yellow", steadyWindow(Window{Valid: true, TmaxF: 80}), Yellow, 0, "hot"},
		{"hot red", steadyWindow(Window{Valid: true, TmaxF: 95}), Red, 0, "too hot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Score(tc.window, p, "AM", th)
			if got.Verdict != tc.grade || got.Reason != tc.reason {
				t.Fatal(got)
			}
			grades := metricGrades(tc.window, p, "AM", th)
			if grades[tc.metric] != tc.grade {
				t.Fatal(grades)
			}
		})
	}
	w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.004})
	data := windowData(w, Score(w, p, "AM", th), metricGrades(w, p, "AM", th))
	if strings.Split(data, ",")[3] != "dry" {
		t.Fatal(data)
	}
	// A red metric must beat an earlier yellow condition.
	w.WindMaxMph = 35
	w = steadyWindow(w)
	if got := Score(w, p, "AM", th); got.Verdict != Red || got.Reason != "too windy" {
		t.Fatal(got)
	}
}
