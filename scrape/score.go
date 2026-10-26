package scrape

import (
	"strings"
	"time"
)

// Verdict is the single per-window rating.
type Verdict int

const (
	Green Verdict = iota
	Yellow
	Red
)

// Result is the verdict plus a short reason (used later by the tap-detail bar).
type Result struct {
	Verdict Verdict
	Reason  string
}

// Profile is the crag-specific context that transforms raw weather into "felt"
// conditions. Built from config (aspect, rock type, seepage).
type Profile struct {
	Aspect       string  // "sun all day" | "AM sun" | "PM sun" | "shade" | "mixed"
	DryoutInches float64 // antecedent rain (in 72h) at which this rock reads wet
	Seepage      bool    // seeps after wet spells regardless of direct rain
}

// Thresholds are every tunable constant in one place so the model can be
// retuned without touching logic. These are v1 strawmen — calibrate against a
// fixture table of known days.
type Thresholds struct {
	RainYellow              float64 // in-window rain at/above → Yellow
	WetPrecip               float64 // rain amount (in) needed for Red when likely or probability is unknown
	RainPossibleProbability float64
	RainLikelyProbability   float64
	RainConflictAmount      float64 // low probability with this much rain still warns
	SeepDryout              float64 // antecedent (in) that makes a seeping crag Red
	DryingFrac              float64 // fraction of dryout below which it's "still drying" (Yellow)
	MaxSolarGain            float64 // °F added to a fully-sunny, cloudless window
	HotRed                  float64 // effective temp at/above → Red
	HotYellow               float64 // → Yellow
	HotFloor                float64 // air temp (°F) below which a window never reads "hot"
	ColdRed                 float64 // at/below → Red
	ColdYellow              float64 // → Yellow
	DewRed                  float64 // dew point at/above → Red
	DewYellow               float64 // dew point at/above → Yellow (greasy friction)
	AQIYellow               float64 // AQI at or above this value → Yellow
	AQIRed                  float64 // AQI at or above this value → Red
	WindRed                 float64 // wind at/above → Red
	WindYellow              float64 // wind at/above → Yellow
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		RainYellow:              0.01,
		WetPrecip:               0.02,
		RainPossibleProbability: 20,
		RainLikelyProbability:   60,
		RainConflictAmount:      0.10,
		SeepDryout:              0.10,
		DryingFrac:              0.5,
		MaxSolarGain:            14,
		HotRed:                  95,
		HotYellow:               80,
		HotFloor:                70,
		ColdRed:                 28,
		ColdYellow:              36,
		DewYellow:               60,
		DewRed:                  70, // Very humid: weather.gov/tbw/dewpoint. Product threshold, not a safety limit.
		WindYellow:              25,
		WindRed:                 35, // Within NWS advisory sustained-wind range: weather.gov/box/criteria.
		AQIYellow:               100,
		AQIRed:                  150,
	}
}

// Score summarizes six hourly weather ratings, with the existing window-level
// rain and drying result as a conservative minimum severity.
func Score(w Window, p Profile, win string, t Thresholds) Result {
	return Explain(w, p, win, t).Result
}

// Explanation is the evidence behind a window rating, so the details can show
// exactly what limited it instead of independently graded extremes.
type Explanation struct {
	Result
	Hours  string   // when the limiting condition occurs, e.g. "until 10am"; empty for the whole window
	FeelsF *float64 // sun-adjusted extreme behind a temperature rating
	TempF  float64  // air temperature in the hour of FeelsF
}

// Explain scores a window from its hourly observations and records which
// hours and measurement determined the rating.
func Explain(w Window, p Profile, win string, t Thresholds) Explanation {
	if !w.Valid || len(w.hours) != 6 {
		return Explanation{Result: Result{Yellow, "no data"}}
	}
	var ratings []Result
	var goodRun, longestGood, poor int
	for i, h := range w.hours {
		if i > 0 && h.Time.Sub(w.hours[i-1].Time) != time.Hour {
			return Explanation{Result: Result{Yellow, "no data"}}
		}
		r := scoreHour(h, p, win, t)
		ratings = append(ratings, r)
		if r.Verdict == Green {
			goodRun++
			longestGood = max(longestGood, goodRun)
		} else {
			goodRun = 0
		}
		if r.Verdict == Red {
			poor++
		}
	}
	result := dominantReason(ratings)
	switch {
	case longestGood >= 4 && poor == 0:
		result = Result{Green, "good"}
	case poor >= 4:
		result.Verdict = Red
	default:
		result.Verdict = Yellow
	}
	wetness := rainDryingResult(w, metricGrades(w, p, win, t))
	if wetness.Verdict != Green && wetness.Verdict >= result.Verdict {
		return Explanation{Result: wetness}
	}
	e := Explanation{Result: result}
	if result.Verdict == Green {
		return e
	}
	first, last := -1, -1
	for i, r := range ratings {
		if r.Verdict != Green && reasonMetric(r.Reason) == reasonMetric(result.Reason) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	e.Hours = hourSpan(w.hours, first, last)
	if reasonMetric(result.Reason) == MetricTemp {
		hot := strings.HasSuffix(result.Reason, "hot")
		for _, h := range w.hours[first : last+1] {
			eff := h.TempF + sunFactor(p.Aspect, win)*t.MaxSolarGain*(1-h.Cloud/100)
			if e.FeelsF == nil || hot && eff > *e.FeelsF || !hot && eff < *e.FeelsF {
				e.FeelsF, e.TempF = &eff, h.TempF
			}
		}
	}
	return e
}

// hourSpan names the local hours from first through last, or "" when they
// cover the whole window.
func hourSpan(hours []Hour, first, last int) string {
	if first < 0 || first == 0 && last == len(hours)-1 {
		return ""
	}
	clock := func(t time.Time) string { return t.Format("3pm") }
	end := hours[last].Time.Add(time.Hour)
	switch {
	case first == 0:
		return "until " + clock(end)
	case last == len(hours)-1:
		return "after " + clock(hours[first].Time)
	default:
		return clock(hours[first].Time) + "–" + clock(end)
	}
}

// Metric is a detail measurement that can explain a rating.
type Metric int

const (
	MetricNone Metric = iota
	MetricTemp
	MetricRain
	MetricPastRain
	MetricDew
	MetricWind
	MetricAQI
)

func reasonMetric(reason string) Metric {
	switch {
	case reason == "hot" || reason == "too hot" || reason == "cold" || reason == "too cold":
		return MetricTemp
	case strings.HasPrefix(reason, "rain "):
		return MetricRain
	case reason == "drying" || reason == "wet rock":
		return MetricPastRain
	case reason == "humid" || reason == "very humid":
		return MetricDew
	case reason == "windy" || reason == "too windy":
		return MetricWind
	case reason == "air quality":
		return MetricAQI
	default:
		return MetricNone
	}
}

// scoreHour uses simultaneous observations. Rain and drying are deliberately
// excluded here: their thresholds still describe the entire climbing window.
func scoreHour(h Hour, p Profile, win string, t Thresholds) Result {
	w := Window{Valid: true, TmaxF: h.TempF, DewMaxF: h.DewF,
		WindMaxMph: h.WindMph, CloudMean: h.Cloud, AQI: h.AQI}
	grades := metricGrades(w, p, win, t)
	result := Result{Green, "good"}
	for _, severity := range []Verdict{Red, Yellow} {
		for _, metric := range []int{5, 0, 3, 4} {
			if grades[metric] != severity {
				continue
			}
			reason := ""
			switch metric {
			case 0:
				reason = "cold"
				if h.TempF >= t.HotFloor {
					reason = "hot"
				}
				if severity == Red {
					reason = "too " + reason
				}
			case 3:
				reason = "humid"
				if severity == Red {
					reason = "very humid"
				}
			case 4:
				reason = "windy"
				if severity == Red {
					reason = "too windy"
				}
			case 5:
				reason = "air quality"
			}
			return Result{severity, reason}
		}
	}
	return result
}

// dominantReason chooses frequency, then severity, then the existing metric
// priority. Iteration order preserves the earliest occurrence for remaining ties.
func dominantReason(ratings []Result) Result {
	counts := map[Result]int{}
	for _, r := range ratings {
		if r.Verdict != Green {
			counts[r]++
		}
	}
	best := Result{Green, "good"}
	for _, r := range ratings {
		if r.Verdict == Green {
			continue
		}
		if counts[r] > counts[best] || counts[r] == counts[best] &&
			(r.Verdict > best.Verdict || r.Verdict == best.Verdict && reasonPriority(r.Reason) < reasonPriority(best.Reason)) {
			best = r
		}
	}
	return best
}

func reasonPriority(reason string) int {
	switch reason {
	case "air quality":
		return 0
	case "hot", "too hot", "cold", "too cold":
		return 1
	case "humid", "very humid":
		return 2
	default:
		return 3 // wind
	}
}

// conditionSummary is shared by the expanded Conditions row and cell labels.
func conditionSummary(r Result) string {
	switch {
	case r.Reason == "no data":
		return "No data"
	case r.Verdict == Green:
		return "Good"
	case r.Verdict == Yellow:
		return "Marginal · " + r.Reason
	default:
		return "Poor · " + r.Reason
	}
}

// explanationSummary adds when the limiting condition occurs.
func explanationSummary(e Explanation) string {
	text := conditionSummary(e.Result)
	if e.Verdict != Green && e.Hours != "" {
		text += " " + e.Hours
	}
	return text
}

// worseResult prefers air quality on ties, then rain over drying.
func worseResult(a, b Result) Result {
	if b.Verdict > a.Verdict || (b.Verdict == a.Verdict && b.Reason == "air quality") {
		return b
	}
	if b.Verdict == a.Verdict && strings.HasPrefix(b.Reason, "rain ") &&
		(a.Reason == "drying" || a.Reason == "wet rock") {
		return b
	}
	return a
}

// AreaVerdict rolls the per-crag day verdicts of an area into one summary
// verdict by majority: Green only when more than half the crags are green; a
// single green crag is enough to keep an otherwise-poor day at Yellow; Red only
// when a majority are red and none are green. An empty area reads Yellow.
func AreaVerdict(vs []Verdict) Verdict {
	var green, red int
	for _, v := range vs {
		switch v {
		case Green:
			green++
		case Red:
			red++
		}
	}
	n := len(vs)
	switch {
	case green*2 > n:
		return Green
	case green > 0:
		return Yellow
	case red*2 > n:
		return Red
	default:
		return Yellow
	}
}

// sunFactor is how much direct sun this aspect gets in the given window (0–1).
// It's what makes sun an asset on cold days and a liability on hot ones, since
// it only ever shifts the effective temperature.
func sunFactor(aspect, win string) float64 {
	switch aspect {
	case "sun all day":
		return 1
	case "shade":
		return 0
	case "mixed":
		return 0.5
	case "AM sun":
		if win == "AM" {
			return 1
		}
		return 0.1
	case "PM sun":
		if win == "PM" {
			return 1
		}
		return 0.1
	}
	return 0.3 // unknown aspect → mild default
}

// DryoutFor maps a rock type to its 72h antecedent threshold. Slow-drying rock
// (conglomerate, sandstone) reads wet from less rain; well-draining rock needs
// more. Tunable, like everything else here.
func DryoutFor(rock string) float64 {
	switch rock {
	case "sandstone", "conglomerate":
		return 0.20
	case "granite", "gneiss":
		return 0.40
	case "limestone":
		return 0.45
	case "basalt", "rhyolite", "welded tuff":
		return 0.55
	}
	return 0.40
}

// metricGrades rates each detail metric independently, so a worse condition
// does not hide other warnings. Order: temperature, rain, soak, dew, wind, AQI.
func metricGrades(w Window, p Profile, win string, t Thresholds) [6]Verdict {
	var grades [6]Verdict
	if !w.Valid {
		return grades
	}
	eff := w.TmaxF + sunFactor(p.Aspect, win)*t.MaxSolarGain*(1-w.CloudMean/100)
	hot := w.TmaxF >= t.HotFloor
	switch {
	case hot && eff >= t.HotRed || eff <= t.ColdRed:
		grades[0] = Red
	case hot && eff >= t.HotYellow || eff <= t.ColdYellow:
		grades[0] = Yellow
	}
	grades[1] = rainGrade(w, t)
	dryout := p.DryoutInches
	if p.Seepage {
		dryout = t.SeepDryout
	}
	switch {
	case w.Antecedent >= dryout:
		grades[2] = Red
	case w.Antecedent >= dryout*t.DryingFrac:
		grades[2] = Yellow
	}
	switch {
	case w.DewMaxF >= t.DewRed:
		grades[3] = Red
	case w.DewMaxF >= t.DewYellow:
		grades[3] = Yellow
	}
	switch {
	case w.WindMaxMph >= t.WindRed:
		grades[4] = Red
	case w.WindMaxMph >= t.WindYellow:
		grades[4] = Yellow
	}
	if w.AQI != nil {
		switch {
		case *w.AQI >= t.AQIRed:
			grades[5] = Red
		case *w.AQI >= t.AQIYellow:
			grades[5] = Yellow
		}
	}
	return grades
}

func rainGrade(w Window, t Thresholds) Verdict {
	if w.PrecipProbability == nil {
		switch {
		case w.PrecipIn >= t.WetPrecip:
			return Red
		case w.PrecipIn >= t.RainYellow:
			return Yellow
		default:
			return Green
		}
	}
	switch {
	case *w.PrecipProbability < t.RainPossibleProbability:
		if w.PrecipIn >= t.RainConflictAmount {
			return Yellow
		}
		return Green
	case *w.PrecipProbability >= t.RainLikelyProbability && w.PrecipIn >= t.WetPrecip:
		return Red
	default:
		return Yellow
	}
}

// wetnessResult combines rain, drying, and humidity without treating humid air
// as a prediction of wet rock. Rain and drying win equally severe warnings.
func wetnessResult(w Window, grades [6]Verdict) Result {
	wetness := rainDryingResult(w, grades)
	if w.Valid && grades[3] > wetness.Verdict {
		reason := "humid"
		if grades[3] == Red {
			reason = "very humid"
		}
		return Result{grades[3], reason}
	}
	return wetness
}

// rainDryingResult preserves the existing overall scoring reason priority.
func rainDryingResult(w Window, grades [6]Verdict) Result {
	if !w.Valid {
		return Result{Yellow, "—"}
	}
	if grades[1] >= grades[2] && grades[1] != Green {
		reason := "rain possible"
		if grades[1] == Red {
			reason = "rain likely"
			if w.PrecipProbability == nil {
				reason = "rain forecast"
			}
		}
		return Result{grades[1], reason}
	}
	switch grades[2] {
	case Red:
		return Result{Red, "wet rock"}
	case Yellow:
		return Result{Yellow, "drying"}
	default:
		return Result{Green, "dry"}
	}
}

// worseWetness selects severity first, then rain, drying, and humidity.
// A valid window wins over a missing one; otherwise the first wins equal ties.
func worseWetness(a, b Result) Result {
	if a.Reason == "—" {
		return b
	}
	if b.Reason == "—" {
		return a
	}
	if b.Verdict > a.Verdict || b.Verdict == a.Verdict &&
		wetnessPriority(b.Reason) > wetnessPriority(a.Reason) {
		return b
	}
	return a
}

func wetnessPriority(reason string) int {
	switch {
	case strings.HasPrefix(reason, "rain "):
		return 3
	case reason == "drying" || reason == "wet rock":
		return 2
	case reason == "humid" || reason == "very humid":
		return 1
	default:
		return 0
	}
}
