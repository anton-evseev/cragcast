package scrape

import (
	"fmt"
	"math"
	"time"
)

// These are uncalibrated product estimates, not measured rock moisture.
const (
	surfaceSaturation = 0.10 // inches of rain per unit of surface wetness
	surfaceCap        = 2.0  // heavy rain needs one extra half-life to dry
	seepSaturation    = 1.0  // inches; a multi-day wet spell, not one shower
	seepHalfLife      = 72.0 // elapsed hours
)

type moisture struct {
	surface float64
	seep    float64
}

// validateWarmup requires seven calendar days of hourly weather before today.
// Elapsed-hour iteration preserves both occurrences of a repeated DST hour.
func validateWarmup(hours []Hour, now time.Time) error {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	coverage := make(map[int64]int, len(hours))
	for i, h := range hours {
		if i > 0 && h.Time.Sub(hours[i-1].Time) != time.Hour {
			return fmt.Errorf("weather timestamps are not consecutive hours")
		}
		coverage[h.Time.Unix()]++
	}
	for ts := today.AddDate(0, 0, -7); !ts.After(today); ts = ts.Add(time.Hour) {
		if coverage[ts.Unix()] != 1 {
			return fmt.Errorf("missing seven-day weather history at %s", ts.Format(time.RFC3339))
		}
	}
	return nil
}

// modelWetness copies shared weather before attaching a crag's state. Start
// dry, then advance through every interval, including nights, before scoring.
// Precipitation and radiation at h.Time describe the interval ending there.
func modelWetness(hours []Hour, p Profile) []Hour {
	out := append([]Hour(nil), hours...)
	var state moisture
	for i := range out {
		if i > 0 {
			h, prev := out[i], out[i-1]
			dt := h.Time.Sub(prev.Time).Hours()
			// Average endpoint weather; radiation belongs to this exact interval.
			driver := prev
			driver.TempF = (prev.TempF + h.TempF) / 2
			driver.DewF = (prev.DewF + h.DewF) / 2
			driver.WindMph = (prev.WindMph + h.WindMph) / 2
			driver.Time = prev.Time.Add(h.Time.Sub(prev.Time) / 2)
			driver.Radiation = h.Radiation
			state.surface = min(surfaceCap, state.surface*math.Exp2(-dt/surfaceHalfLife(driver, p))+h.Precip/surfaceSaturation)
			if p.Seepage {
				state.seep = min(1, state.seep*math.Exp2(-dt/seepHalfLife)+h.Precip/seepSaturation)
			}
		}
		out[i].wetness = state
	}
	return out
}

func clamp(v, lo, hi float64) float64 { return max(lo, min(v, hi)) }

func surfaceHalfLife(h Hour, p Profile) float64 {
	kPa := func(f float64) float64 {
		c := (f - 32) * 5 / 9
		return 0.6108 * math.Exp(17.27*c/(c+237.3))
	}
	vpd := max(0, kPa(h.TempF)-kPa(h.DewF))
	exposed := 0.0
	if h.Radiation != nil {
		win := "AM"
		if h.Time.Hour() >= 13 {
			win = "PM"
		}
		factor := sunFactor(p.Aspect, win)
		if p.Aspect == "mixed" {
			factor = 0.3
		}
		exposed = max(0, *h.Radiation) * factor
	}
	strength := 0.25 + clamp(vpd/0.6, 0, 2)*
		(0.5+0.5*clamp(h.WindMph/10, 0, 1))*(0.5+clamp(exposed/400, 0, 1))
	rock := p.DryingFactor
	if rock <= 0 {
		rock = DryoutFor("")
	}
	return clamp(12*(0.40/rock)/strength, 3, 48)
}

func moistureGrade(v float64) Verdict {
	switch {
	case v >= 0.5:
		return Red
	case v >= 0.25:
		return Yellow
	default:
		return Green
	}
}

func moistureResult(m moisture) Result {
	surface := Result{moistureGrade(m.surface), "dry"}
	if surface.Verdict == Yellow {
		surface.Reason = "drying"
	} else if surface.Verdict == Red {
		surface.Reason = "wet likely"
	}
	seep := Result{moistureGrade(m.seep), "dry"}
	if seep.Verdict == Yellow {
		seep.Reason = "seepage possible"
	} else if seep.Verdict == Red {
		seep.Reason = "seepage likely"
	}
	return worseResult(surface, seep)
}

// durationResult is shared by the combined score and individual model rows.
func durationResult(ratings []Result) Result {
	var run, longest, poor int
	for _, r := range ratings {
		if r.Verdict == Green {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
		if r.Verdict == Red {
			poor++
		}
	}
	r := dominantReason(ratings)
	switch {
	case longest >= 4 && poor == 0:
		return Result{Green, "good"}
	case poor >= 4:
		r.Verdict = Red
	default:
		r.Verdict = Yellow
	}
	return r
}

// moistureWindow optionally isolates one state for the detail rows.
func moistureWindow(w Window, component string) Result {
	if !w.Valid || len(w.hours) != 6 {
		return Result{Green, "dry"}
	}
	ratings := make([]Result, len(w.hours))
	for i, h := range w.hours {
		m := h.wetness
		switch component {
		case "surface":
			m.seep = 0
		case "seep":
			m.surface = 0
		}
		ratings[i] = moistureResult(m)
	}
	return durationResult(ratings)
}
