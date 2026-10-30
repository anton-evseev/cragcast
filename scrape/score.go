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
	DryingFactor float64 // relative rock drying speed, see DryoutFor
	Seepage      bool    // seeps after wet spells regardless of direct rain
}

// Thresholds are tunable weather cutoffs. Moisture model coefficients live in
// wetness.go. Product estimates need calibration against observed conditions.
type Thresholds struct {
	RainYellow              float64 // in-window rain at/above → Yellow
	WetPrecip               float64 // rain amount (in) needed for Red when likely or probability is unknown
	RainPossibleProbability float64
	RainLikelyProbability   float64
	RainConflictAmount      float64 // low probability with this much rain still warns
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
// rain result as a conservative minimum severity.
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
	for i, h := range w.hours {
		if i > 0 && h.Time.Sub(w.hours[i-1].Time) != time.Hour {
			return Explanation{Result: Result{Yellow, "no data"}}
		}
		ratings = append(ratings, scoreHour(h, p, win, t))
	}
	result := durationResult(ratings)
	// Only forecast rain retains a whole-window floor.
	rain := forecastRainResult(w, rainGrade(w, t))
	if rain.Verdict != Green && worseResult(result, rain) == rain {
		return Explanation{Result: rain}
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
	MetricSeepage
)

func reasonMetric(reason string) Metric {
	switch {
	case reason == "hot" || reason == "too hot" || reason == "cold" || reason == "too cold":
		return MetricTemp
	case strings.HasPrefix(reason, "rain "):
		return MetricRain
	case reason == "drying" || reason == "wet likely":
		return MetricPastRain
	case strings.HasPrefix(reason, "seepage "):
		return MetricSeepage
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

// scoreHour combines simultaneous weather and the current modeled moisture.
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
			return worseResult(Result{severity, reason}, moistureResult(h.wetness))
		}
	}
	return worseResult(result, moistureResult(h.wetness))
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
	switch reasonMetric(reason) {
	case MetricAQI:
		return 0
	case MetricRain:
		return 1
	case MetricSeepage:
		return 2
	case MetricPastRain:
		return 3
	case MetricTemp:
		return 4
	case MetricDew:
		return 5
	case MetricWind:
		return 6
	default:
		return 7
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

// worseResult selects severity, then the shared reason priority.
func worseResult(a, b Result) Result {
	if b.Verdict > a.Verdict || b.Verdict == a.Verdict && reasonPriority(b.Reason) < reasonPriority(a.Reason) {
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

// DryoutFor supplies the relative rock factor used by the surface drying model.
// The values are product estimates; they are no longer rainfall thresholds.
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
	grades[2] = moistureWindow(w, "").Verdict
	switch {
	case w.DewMaxF >= t.DewRed:
		grades[3] = Red
	case w.DewMaxF >= t.DewYellow:
		grades[3] = Yellow
	}
	if len(w.hours) == 6 {
		ratings := make([]Result, len(w.hours))
		for i, h := range w.hours {
			ratings[i] = Result{Green, "dry"}
			if h.DewF >= t.DewRed {
				ratings[i] = Result{Red, "very humid"}
			} else if h.DewF >= t.DewYellow {
				ratings[i] = Result{Yellow, "humid"}
			}
		}
		grades[3] = durationResult(ratings).Verdict
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

// rainDryingResult uses the same hourly moisture duration policy as Score.
func rainDryingResult(w Window, grades [6]Verdict) Result {
	if !w.Valid {
		return Result{Yellow, "—"}
	}
	r := moistureWindow(w, "")
	if r.Verdict == Green {
		r = Result{Green, "dry"}
	}
	return worseResult(r, forecastRainResult(w, grades[1]))
}

func forecastRainResult(w Window, grade Verdict) Result {
	r := Result{grade, "dry"}
	if grade == Yellow {
		r.Reason = "rain possible"
	} else if grade == Red {
		r.Reason = "rain likely"
		if w.PrecipProbability == nil {
			r.Reason = "rain forecast"
		}
	}
	return r
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
		return 4
	case strings.HasPrefix(reason, "seepage "):
		return 3
	case reason == "drying" || reason == "wet likely":
		return 2
	case reason == "humid" || reason == "very humid":
		return 1
	default:
		return 0
	}
}
