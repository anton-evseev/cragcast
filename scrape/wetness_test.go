package scrape

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProbabilityRainGrades(t *testing.T) {
	for _, tc := range []struct {
		name        string
		amount      float64
		probability *float64
		grade       Verdict
		status      string
	}{
		{"zero", 0, value(0), Green, "dry"},
		{"Icicle Creek", 0.036, value(4), Green, "dry"},
		{"Tumwater Canyon", 0.024, value(5), Green, "dry"},
		{"below conflict", 0.0999, value(19.9), Green, "dry"},
		{"conflicting amount", 0.10, value(19.9), Yellow, "rain possible"},
		{"possible boundary", 0, value(20), Yellow, "rain possible"},
		{"possible large amount", 1, value(59.9), Yellow, "rain possible"},
		{"likely zero amount", 0, value(60), Yellow, "rain possible"},
		{"likely small amount", 0.0199, value(100), Yellow, "rain possible"},
		{"likely boundary", 0.02, value(60), Red, "rain likely"},
		{"certain wet", 1, value(100), Red, "rain likely"},
		{"missing dry", 0.0099, nil, Green, "dry"},
		{"missing yellow", 0.01, nil, Yellow, "rain possible"},
		{"missing red", 0.02, nil, Red, "rain forecast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: tc.amount, PrecipProbability: tc.probability})
			p := Profile{Aspect: "shade", DryoutInches: 0.4}
			th := DefaultThresholds()
			grades := metricGrades(w, p, "AM", th)
			wet := wetnessResult(w, grades)
			if grades[1] != tc.grade || wet.Reason != tc.status {
				t.Fatalf("grade=%v wetness=%+v", grades[1], wet)
			}
			score := Score(w, p, "AM", th)
			if score.Verdict != tc.grade || tc.grade != Green && score.Reason != wet.Reason {
				t.Fatal(score)
			}
			if tc.grade != Green && iconOf(score, w.CloudMean) != "i-rain" {
				t.Fatal("rain warning has wrong icon")
			}
			fields := strings.Split(windowData(w, score, grades), ",")
			if len(fields) != 13 || fields[3] != tc.status || strings.Split(fields[6], "|")[1] != classOf(tc.grade) {
				t.Fatal(fields)
			}
		})
	}
}

func TestHumidityWetness(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	for _, tc := range []struct {
		dew  float64
		want Result
	}{
		{59.9, Result{Green, "dry"}},
		{60, Result{Yellow, "humid"}},
		{69.9, Result{Yellow, "humid"}},
		{70, Result{Red, "very humid"}},
	} {
		w := steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: tc.dew})
		grades := metricGrades(w, p, "AM", th)
		if got := wetnessResult(w, grades); got != tc.want {
			t.Fatalf("dew=%v: got %+v, want %+v", tc.dew, got, tc.want)
		}
		fields := strings.Split(windowData(w, Score(w, p, "AM", th), grades), ",")
		if len(fields) != 13 || fields[3] != tc.want.Reason || fields[6] != "go|"+classOf(tc.want.Verdict)+"|go|go" {
			t.Fatal(fields)
		}
	}
	w := steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 70, PrecipIn: 0.01, Antecedent: 0.2})
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got != (Result{Red, "very humid"}) {
		t.Fatal(got)
	}
	w.DewMaxF = 60
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got.Reason != "rain possible" {
		t.Fatal(got)
	}
	w.PrecipIn = 0
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got.Reason != "drying" {
		t.Fatal(got)
	}
	w.DewMaxF, w.Antecedent = 70, 0.4
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got.Reason != "wet rock" {
		t.Fatal(got)
	}
	// Combining the displayed metrics must not move humidity ahead of heat
	// in the overall score or change its icon.
	w = steadyWindow(Window{Valid: true, TmaxF: 95, DewMaxF: 70})
	if got := Score(w, p, "AM", th); got != (Result{Red, "too hot"}) || iconOf(got, w.CloudMean) != "i-sun" {
		t.Fatal(got)
	}
}

func TestDailyHumidityWetness(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	for _, tc := range []struct {
		am, pm Window
		want   Result
	}{
		{steadyWindow(Window{Valid: true, TmaxF: 75}), steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 70}), Result{Red, "very humid"}},
		{steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 60}), steadyWindow(Window{Valid: true, TmaxF: 75, Antecedent: 0.2}), Result{Yellow, "drying"}},
		{steadyWindow(Window{Valid: true, TmaxF: 75, Antecedent: 0.2}), steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 60}), Result{Yellow, "drying"}},
		{steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 70}), steadyWindow(Window{Valid: true, TmaxF: 75, PrecipIn: 0.02}), Result{Red, "rain forecast"}},
		{Window{}, steadyWindow(Window{Valid: true, TmaxF: 75, DewMaxF: 60}), Result{Yellow, "humid"}},
		{steadyWindow(Window{Valid: true, TmaxF: 75}), Window{}, Result{Green, "dry"}},
		{Window{}, Window{}, Result{Green, "—"}}, // no data: nothing to highlight
	} {
		cd := cragDays([]DayForecast{{AM: tc.am, PM: tc.pm}}, p, th)[0]
		if cd.Wetness != tc.want.Reason || strings.Split(cd.Highlights, "|")[1] != classOf(tc.want.Verdict) {
			t.Fatalf("AM=%+v PM=%+v: got %+v, want %+v", tc.am, tc.pm, cd, tc.want)
		}
	}
	if got := worseWetness(Result{Red, "rain forecast"}, Result{Red, "rain likely"}); got.Reason != "rain forecast" {
		t.Fatal("equal priority must retain AM:", got)
	}
	fields := strings.Split(windowData(Window{}, Result{Yellow, "no data"}, [6]Verdict{}), ",")
	if len(fields) != 13 {
		t.Fatal(fields)
	}
}

func TestWetnessPrecedence(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.1, PrecipProbability: value(40), Antecedent: 0.4})
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got.Reason != "wet rock" {
		t.Fatal(got)
	}
	w.Antecedent = 0.2
	if got := wetnessResult(w, metricGrades(w, p, "AM", th)); got.Reason != "rain possible" {
		t.Fatal(got)
	}
	w.PrecipProbability = value(60)
	w.Antecedent = 0.4
	if got := Score(w, p, "AM", th); got.Reason != "rain likely" {
		t.Fatal(got)
	}
	am := steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.4})
	pm := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.1, PrecipProbability: value(60)})
	day := cragDays([]DayForecast{{AM: am, PM: pm}}, p, th)[0]
	if day.Wetness != "rain likely" || day.Reason != "rain likely" || strings.Split(day.Highlights, "|")[1] != "wet" {
		t.Fatal(day)
	}
	if got := worseWetness(Result{Yellow, "rain possible"}, Result{Yellow, "rain possible"}); got.Reason != "rain possible" {
		t.Fatal(got)
	}
	if got := wetnessResult(Window{}, [6]Verdict{}); got.Reason != "—" {
		t.Fatal(got)
	}
}

func TestPrecipitationIntervals(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{
		time.Date(2026, 10, 17, 0, 0, 0, 0, loc),
		time.Date(2026, 3, 8, 0, 0, 0, 0, loc),
		time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
	} {
		start := time.Date(date.Year(), date.Month(), date.Day(), 7, 0, 0, 0, loc)
		end := time.Date(date.Year(), date.Month(), date.Day(), 19, 0, 0, 0, loc)
		from := start.Add(-72 * time.Hour)
		var hours []Hour
		for ts := from; !ts.After(end); ts = ts.Add(time.Hour) {
			h := Hour{Time: ts, TempF: 65, PrecipProbability: value(5)}
			switch {
			case ts.Equal(from):
				h.Precip = 10
			case ts.Equal(start):
				h.Precip = 0.1
				h.PrecipProbability = value(99)
			case ts.Equal(start.Add(6 * time.Hour)):
				h.Precip = 0.02
				h.PrecipProbability = value(40)
			case ts.Equal(end):
				h.Precip = 0.03
				h.PrecipProbability = value(60)
			}
			hours = append(hours, h)
		}
		day := Aggregate(hours, loc, date, 1)[0]
		if day.AM.PrecipIn != 0.02 || day.PM.PrecipIn != 0.03 ||
			day.AM.Antecedent != 0.1 || day.PM.Antecedent < 0.1199 || day.PM.Antecedent > 0.1201 ||
			*day.AM.PrecipProbability != 40 || *day.PM.PrecipProbability != 60 {
			t.Fatalf("%s AM=%+v PM=%+v", date, day.AM, day.PM)
		}
		for i := range hours {
			if hours[i].Time.Equal(start.Add(time.Hour)) {
				hours[i].PrecipProbability = nil
			}
		}
		day = Aggregate(hours, loc, date, 1)[0]
		if day.AM.PrecipProbability != nil || day.PM.PrecipProbability == nil {
			t.Fatal("incomplete probability must remain unknown")
		}
	}
}

func TestProbabilityParsing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		probs     any
		omit      bool
		wantError bool
	}{
		{"missing", nil, true, false},
		{"null", []any{nil, 0.0}, false, false},
		{"valid", []float64{4, 100}, false, false},
		{"ragged", []float64{4}, false, true},
		{"negative", []float64{-1, 4}, false, true},
		{"too large", []float64{4, 101}, false, true},
		{"wrong type", "unknown", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := weatherRow([]int64{0, 3600}, 65)
			if !tc.omit {
				row["hourly"].(map[string]any)["precipitation_probability"] = tc.probs
			}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var raw omResponse
			err = json.Unmarshal(data, &raw)
			var hours []Hour
			if err == nil {
				hours, err = toHours(raw, time.UTC)
			}
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if tc.name == "null" && (hours[0].PrecipProbability != nil || hours[1].PrecipProbability == nil || *hours[1].PrecipProbability != 0) {
				t.Fatal("null or zero probability lost")
			}
		})
	}
}

func TestSeparateWetnessDetails(t *testing.T) {
	w := steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.004, PrecipProbability: value(60), Antecedent: 0.45, HumidityMax: value(85), DewMaxF: 61})
	p, th := Profile{Aspect: "shade", DryoutInches: 0.4}, DefaultThresholds()
	fields := strings.Split(windowData(w, Score(w, p, "AM", th), metricGrades(w, p, "AM", th)), ",")
	if got := strings.Join(fields[7:], ","); got != "0.004,60,0.450,85,61,warn|wet|warn" {
		t.Fatal("separate wetness values or highlights lost:", got)
	}
	w.PrecipProbability, w.HumidityMax = nil, nil
	fields = strings.Split(windowData(w, Score(w, p, "AM", th), metricGrades(w, p, "AM", th)), ",")
	if fields[8] != "" || fields[10] != "" {
		t.Fatal("missing values became zero", fields)
	}
	fields = strings.Split(windowData(Window{}, Result{Yellow, "no data"}, [6]Verdict{}), ",")
	for _, field := range fields[2:] {
		if field != "" {
			t.Fatal("missing window has measurements", fields)
		}
	}
}

func TestAreaHighlightsExplainRating(t *testing.T) {
	p, th := Profile{Aspect: "shade", DryoutInches: 0.4}, DefaultThresholds()
	cold := steadyWindow(Window{Valid: true, TmaxF: 44, TminF: 33})
	for i := range 3 {
		cold.hours[i].TempF = 33
	}
	for _, tc := range []struct {
		name, temp, precip, want, summary string
		w                                 Window
		aspect                            string
	}{
		// A trace at low chance rates Good, so nothing may be highlighted.
		{"trace rain at low chance", "65°F", "trace", "go|go|go|go|go|go|go", "Good",
			steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.012, PrecipProbability: value(8)}), ""},
		// A cold start must show in the range, the highlight and the reason.
		{"cold early under a warm high", "33–44°F", "0 in", "warn|go|go|go|go|go|go", "Marginal · cold until 10am", cold, ""},
		{"rain", "65°F", "0.20 in", "go|wet|wet|go|go|go|go", "Poor · rain likely",
			steadyWindow(Window{Valid: true, TmaxF: 65, PrecipIn: 0.2, PrecipProbability: value(80)}), ""},
		{"chance without amount", "65°F", "0 in", "go|go|warn|go|go|go|go", "Marginal · rain possible",
			steadyWindow(Window{Valid: true, TmaxF: 65, PrecipProbability: value(30)}), ""},
		{"drying", "65°F", "0 in", "go|go|go|warn|go|go|go", "Marginal · drying",
			steadyWindow(Window{Valid: true, TmaxF: 65, Antecedent: 0.2}), ""},
		{"humid", "65°F", "0 in", "go|go|go|go|warn|go|go", "Marginal · humid",
			steadyWindow(Window{Valid: true, TmaxF: 65, DewMaxF: 60}), ""},
		{"wind ignores lesser AQI", "65°F", "0 in", "go|go|go|go|go|wet|go", "Poor · too windy",
			steadyWindow(Window{Valid: true, TmaxF: 65, WindMaxMph: 40, AQI: value(110)}), ""},
		{"hot in sun", "85°F · feels 99°F in sun", "0 in", "wet|go|go|go|go|go|go", "Poor · too hot",
			steadyWindow(Window{Valid: true, TmaxF: 85}), "sun all day"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof := p
			if tc.aspect != "" {
				prof.Aspect = tc.aspect
			}
			fields := strings.Split(areaWindowData(tc.w, prof, "AM", th), ",")
			if fields[2] != tc.temp || fields[7] != tc.precip || fields[13] != tc.want || fields[14] != tc.summary {
				t.Fatalf("temp %q precip %q highlights %s summary %q", fields[2], fields[7], fields[13], fields[14])
			}
			// Every non-green rating must highlight something visible; green, nothing.
			highlighted := strings.Contains(fields[13], "warn") || strings.Contains(fields[13], "wet")
			if highlighted != (fields[0] != "go") {
				t.Fatalf("rating %s with highlights %s", fields[0], fields[13])
			}
		})
	}
}
