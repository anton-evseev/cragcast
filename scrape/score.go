package scrape

import "strings"

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
	AQIYellow               float64 // AQI above this value → Yellow
	AQIRed                  float64 // AQI above this value → Red
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
		AQIYellow:               50,
		AQIRed:                  100,
	}
}

// Score is the heart of the app: pure function from aggregated window + crag
// profile to a verdict. Optional AQI can worsen the weather verdict.
func Score(w Window, p Profile, win string, t Thresholds) Result {
	if !w.Valid {
		return Result{Yellow, "no data"}
	}
	grades := metricGrades(w, p, win, t)
	wetness := rainDryingResult(w, grades)
	result := Result{Green, "good"}
	// Preserve weather reason priority while selecting the worst metric grade.
	for _, severity := range []Verdict{Red, Yellow} {
		for _, metric := range []int{1, 0, 3, 4} {
			grade := grades[metric]
			if metric == 1 {
				grade = wetness.Verdict
			}
			if grade != severity {
				continue
			}
			reason := ""
			switch metric {
			case 0:
				reason = "cold"
				if w.TmaxF >= t.HotFloor {
					reason = "hot"
				}
				if severity == Red {
					reason = "too " + reason
				}
			case 1:
				reason = wetness.Reason
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
			}
			result = Result{severity, reason}
			break
		}
		if result.Verdict != Green {
			break
		}
	}
	if grades[5] != Green {
		result = worseResult(result, Result{grades[5], "air quality"})
	}
	return result
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
		case *w.AQI > t.AQIRed:
			grades[5] = Red
		case *w.AQI > t.AQIYellow:
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
