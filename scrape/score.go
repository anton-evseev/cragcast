package scrape

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
	WetPrecip    float64 // in-window rain (in) that gates straight to Red
	SeepDryout   float64 // antecedent (in) that makes a seeping crag Red
	DryingFrac   float64 // fraction of dryout below which it's "still drying" (Yellow)
	MaxSolarGain float64 // °F added to a fully-sunny, cloudless window
	HotRed       float64 // effective temp at/above → Red
	HotYellow    float64 // → Yellow
	HotFloor     float64 // air temp (°F) below which a window never reads "hot"
	ColdRed      float64 // at/below → Red
	ColdYellow   float64 // → Yellow
	DewYellow    float64 // dew point at/above → Yellow (greasy friction)
	WindYellow   float64 // wind at/above → Yellow
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		WetPrecip:    0.02,
		SeepDryout:   0.10,
		DryingFrac:   0.5,
		MaxSolarGain: 14,
		HotRed:       95,
		HotYellow:    80,
		HotFloor:     70,
		ColdRed:      28,
		ColdYellow:   36,
		DewYellow:    60,
		WindYellow:   25,
	}
}

// Score is the heart of the app: pure function from aggregated window + crag
// profile to a verdict. Two stages — a wetness gate that can veto to Red, then
// a comfort score that decides Green vs Yellow.
func Score(w Window, p Profile, win string, t Thresholds) Result {
	if !w.Valid {
		return Result{Yellow, "no data"}
	}

	// --- Stage 1: wetness gate (limiting factor — beats any comfort) ---
	if w.PrecipIn >= t.WetPrecip {
		return Result{Red, "raining"}
	}
	dryout := p.DryoutInches
	if p.Seepage {
		dryout = t.SeepDryout // seeping crags tolerate far less antecedent rain
	}
	if w.Antecedent >= dryout {
		return Result{Red, "wet rock"}
	}
	stillDrying := w.Antecedent >= dryout*t.DryingFrac

	// --- Stage 2: comfort (effective temperature + friction) ---
	// Sun and wind act through temperature; fold sun into an effective temp.
	gain := sunFactor(p.Aspect, win) * t.MaxSolarGain * (1 - w.CloudMean/100)
	eff := w.TmaxF + gain
	// Solar gain can only make an already-warm day feel hot — a cool day in
	// the sun never reads "hot". Below HotFloor the heat checks don't apply.
	hot := w.TmaxF >= t.HotFloor

	switch {
	case hot && eff >= t.HotRed:
		return Result{Red, "too hot"}
	case eff <= t.ColdRed:
		return Result{Red, "too cold"}
	}

	switch {
	case stillDrying:
		return Result{Yellow, "drying"}
	case hot && eff >= t.HotYellow:
		return Result{Yellow, "hot"}
	case eff <= t.ColdYellow:
		return Result{Yellow, "cold"}
	case w.DewMaxF >= t.DewYellow:
		return Result{Yellow, "humid"}
	case w.WindMaxMph >= t.WindYellow:
		return Result{Yellow, "windy"}
	}
	return Result{Green, "good"}
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
