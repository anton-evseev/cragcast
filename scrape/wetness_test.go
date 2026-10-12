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
			w := Window{Valid: true, TmaxF: 65, PrecipIn: tc.amount, PrecipProbability: tc.probability}
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
			if tc.grade != Green && iconOf(score) != "i-rain" {
				t.Fatal("rain warning has wrong icon")
			}
			fields := strings.Split(windowData(w, score, grades), ",")
			if len(fields) != 8 || fields[3] != tc.status || strings.Split(fields[7], "|")[1] != classOf(tc.grade) {
				t.Fatal(fields)
			}
		})
	}
}

func TestWetnessPrecedence(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 0.4}
	th := DefaultThresholds()
	w := Window{Valid: true, TmaxF: 65, PrecipIn: 0.1, PrecipProbability: value(40), Antecedent: 0.4}
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
	am := Window{Valid: true, TmaxF: 65, Antecedent: 0.4}
	pm := Window{Valid: true, TmaxF: 65, PrecipIn: 0.1, PrecipProbability: value(60)}
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
