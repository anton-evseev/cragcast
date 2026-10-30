package scrape

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func modelHours(start time.Time, count int) []Hour {
	hours := make([]Hour, count)
	for i := range hours {
		hours[i] = Hour{Time: start.Add(time.Duration(i) * time.Hour), TempF: 42, DewF: 42}
	}
	return hours
}

func TestMoistureAccumulationAndDecay(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: .4, Seepage: true}
	hours := modelHours(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), 171)
	hours[1].Precip = 1
	got := modelWetness(hours, p)
	if got[0].wetness != (moisture{}) || got[1].wetness != (moisture{surfaceCap, 1}) {
		t.Fatalf("dry initialization or saturation: %v %v", got[0].wetness, got[1].wetness)
	}
	// Saturated air has the maximum 48-hour surface half-life; seepage
	// independently has its 72-hour half-life regardless of weather.
	if math.Abs(got[49].wetness.surface-1) > 1e-12 || math.Abs(got[73].wetness.seep-.5) > 1e-12 {
		t.Fatal("surface or seepage decay", got[49].wetness, got[73].wetness)
	}
	if moistureResult(got[169].wetness).Verdict != Green {
		t.Fatal("seven rain-free days should clear both states", got[169].wetness)
	}
	for i := 2; i < 10; i++ {
		hours[i].Precip = .002
	}
	repeated := modelWetness(hours, p)
	if repeated[9].wetness.surface <= got[9].wetness.surface || repeated[9].wetness.seep <= got[9].wetness.seep {
		t.Fatal("repeated rain must replenish moisture")
	}
	hours[170].Precip = .3
	hours[169].TempF, hours[170].TempF = 80, 80
	hours[169].DewF, hours[170].DewF = 20, 20
	fresh := modelWetness(hours, p)
	if fresh[170].wetness.surface != surfaceCap {
		t.Fatal("drying must precede fresh rain", fresh[170].wetness)
	}
	for _, h := range fresh {
		if h.wetness.surface < 0 || h.wetness.surface > surfaceCap || h.wetness.seep < 0 || h.wetness.seep > 1 {
			t.Fatal("state outside bounds", h.wetness)
		}
	}
}

// A trace model hour stays Good; heavy rain needs one more half-life than a
// moderate rain before the surface clears.
func TestRainAmountScalesSurfaceWetness(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: .4}
	hours := modelHours(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), 171)
	hours[1].Precip = .01
	if r := moistureResult(modelWetness(hours, p)[1].wetness); r.Verdict != Green {
		t.Fatal("trace rain should not warn", r)
	}
	clears := func(rain float64) int {
		hours[1].Precip = rain
		for i, h := range modelWetness(hours, p)[1:] {
			if moistureGrade(h.wetness.surface) == Green {
				return i
			}
		}
		return -1
	}
	// Saturated air holds the maximum 48-hour half-life.
	if moderate, heavy := clears(.1), clears(.5); heavy-moderate != 48 {
		t.Fatal("clearing hours", moderate, heavy)
	}
}

// One shower leaves seepage Good; a wet spell makes it likely.
func TestSeepageNeedsWetSpell(t *testing.T) {
	p := Profile{Aspect: "shade", DryingFactor: .4, Seepage: true}
	hours := modelHours(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), 49)
	hours[1].Precip = .1
	if g := moistureGrade(modelWetness(hours, p)[1].wetness.seep); g != Green {
		t.Fatal("single shower should not seep", g)
	}
	hours[1].Precip, hours[25].Precip = .3, .3
	if g := moistureGrade(modelWetness(hours, p)[25].wetness.seep); g != Red {
		t.Fatal("two-day wet spell should seep", g)
	}
}

func TestSurfaceDryingDrivers(t *testing.T) {
	p := Profile{Aspect: "sun all day", DryingFactor: .4}
	base := Hour{Time: time.Date(2026, 10, 29, 10, 30, 0, 0, time.UTC), TempF: 55, DewF: 45, WindMph: 0, Radiation: value(0)}
	for _, tc := range []struct {
		name   string
		change func(*Hour)
	}{
		{"warmer", func(h *Hour) { h.TempF = 65 }},
		{"drier", func(h *Hour) { h.DewF = 35 }},
		{"windier", func(h *Hour) { h.WindMph = 10 }},
		{"sunlight", func(h *Hour) { h.Radiation = value(400) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := base
			tc.change(&h)
			if surfaceHalfLife(h, p) >= surfaceHalfLife(base, p) {
				t.Fatal("driver must speed up drying")
			}
		})
	}
	// Without measured/model radiation, clear sky must not invent sunlight.
	night := base
	night.Time = night.Time.Add(12 * time.Hour)
	night.Radiation = nil
	if surfaceHalfLife(night, p) != surfaceHalfLife(base, p) {
		t.Fatal("nighttime/absent radiation gained solar drying")
	}
	shaded := p
	shaded.Aspect = "shade"
	base.Radiation = value(400)
	if surfaceHalfLife(base, shaded) <= surfaceHalfLife(base, p) {
		t.Fatal("shade must dry more slowly than exposed rock")
	}
	sandstone := p
	sandstone.DryingFactor = DryoutFor("sandstone")
	if surfaceHalfLife(base, sandstone) <= surfaceHalfLife(base, p) {
		t.Fatal("sandstone must dry more slowly than granite")
	}
}

func TestSeepagePersistsAfterSurfaceDries(t *testing.T) {
	hours := modelHours(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), 50)
	for i := range hours {
		hours[i].TempF, hours[i].DewF, hours[i].WindMph = 65, 35, 10
		hours[i].Radiation = value(0)
	}
	hours[1].Precip = 1
	p := Profile{Aspect: "shade", DryingFactor: .4, Seepage: true}
	got := modelWetness(hours, p)
	if moistureGrade(got[35].wetness.surface) != Green || moistureResult(got[35].wetness).Reason != "seepage likely" {
		t.Fatal("seepage must outlast surface drying", got[35].wetness)
	}
	for _, boundary := range []int{24, 48} {
		if got[boundary].wetness.surface <= got[boundary+1].wetness.surface || got[boundary+1].wetness.surface == 0 {
			t.Fatal("moisture disappeared at a rolling-history boundary")
		}
	}
}

func TestMoistureIntervalAlignmentAndIsolation(t *testing.T) {
	p := Profile{Aspect: "AM sun", DryingFactor: .4, Seepage: true}
	hours := modelHours(time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC), 5)
	for i := range hours {
		hours[i].TempF, hours[i].DewF = 65, 40
	}
	hours[1].Precip = .02
	hours[2].Radiation = value(400) // noon–1pm is still AM exposure
	got := modelWetness(hours, p)
	allSun := p
	allSun.Aspect = "sun all day"
	comparison := modelWetness(hours, allSun)
	if got[2].wetness.surface != comparison[2].wetness.surface {
		t.Fatal("1pm radiation belongs to the preceding AM hour")
	}
	hours[4].Precip = 1
	hours[4].Radiation = value(900)
	future := modelWetness(hours, p)
	if future[2].wetness != got[2].wetness {
		t.Fatal("future weather changed an earlier estimate")
	}
	nonSeeping := p
	nonSeeping.Seepage = false
	dry := modelWetness(hours, nonSeeping)
	if dry[2].wetness.seep != 0 || got[2].wetness.seep == 0 || hours[2].wetness != (moisture{}) {
		t.Fatal("crag states leaked into shared weather")
	}
	// Probability affects forecast-rain scoring, not modeled rain accumulation.
	hours[1].PrecipProbability = value(0)
	if modelWetness(hours, p)[1].wetness != got[1].wetness {
		t.Fatal("precipitation probability scaled the moisture input")
	}
}

func TestMoistureDurationAndReasonPriority(t *testing.T) {
	p, th := Profile{Aspect: "shade", DryingFactor: .4, Seepage: true}, DefaultThresholds()
	for _, tc := range []struct {
		pattern string
		want    Verdict
	}{
		{"GGGGYY", Green}, {"GGGYYY", Yellow}, {"GGGGRR", Yellow}, {"RRRRYY", Red},
	} {
		w := steadyWindow(Window{Valid: true, TmaxF: 65})
		for i, c := range tc.pattern {
			if c == 'Y' {
				w.hours[i].wetness.surface = .25
			}
			if c == 'R' {
				w.hours[i].wetness.surface = .5
			}
		}
		if got := Score(w, p, "AM", th); got.Verdict != tc.want {
			t.Fatalf("%s: %v", tc.pattern, got)
		}
	}
	w := withWetness(steadyWindow(Window{Valid: true, TmaxF: 95, PrecipIn: .1}), .5, .5)
	if got := Score(w, p, "AM", th); got.Reason != "rain forecast" {
		t.Fatal(got)
	}
	w.PrecipIn = 0
	if got := Score(w, p, "AM", th); got.Reason != "seepage likely" {
		t.Fatal(got)
	}
	w = withWetness(w, .5, 0)
	if got := Score(w, p, "AM", th); got.Reason != "wet likely" {
		t.Fatal(got)
	}
	w.AQI = value(150)
	for i := range w.hours {
		w.hours[i].AQI = w.AQI
	}
	if got := Score(w, p, "AM", th); got.Reason != "air quality" {
		t.Fatal(got)
	}
}

func TestHistoryTotalsAreNeutralContext(t *testing.T) {
	p, th := Profile{Aspect: "shade", DryingFactor: .4}, DefaultThresholds()
	w := steadyWindow(Window{Valid: true, TmaxF: 65, RecentPrecip: .19, Antecedent: .19, HumidityMax: value(95)})
	for _, state := range []float64{0, .25, .5} {
		w = withWetness(w, state, 0)
		fields := strings.Split(areaWindowData(w, p, "AM", th), ",")
		if fields[16] != "go|go" || strings.Split(fields[13], "|")[3] != "go" || fields[15] != "0.190" {
			t.Fatal("same totals inherited different warnings", fields)
		}
		if fields[19] != classOf(moistureGrade(state))+"|go" {
			t.Fatal("modeled evidence missing", fields)
		}
	}
	// Crossing 24h and 72h lookback boundaries does not change a state or score.
	w = withWetness(w, .3, 0)
	before := Score(w, p, "AM", th)
	w.RecentPrecip, w.Antecedent = 0, 0
	if Score(w, p, "AM", th) != before {
		t.Fatal("rolling total caused a rating cliff")
	}
}

func TestSevenDayWarmupAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{
		time.Date(2026, 3, 9, 0, 0, 0, 0, loc),
		time.Date(2026, 11, 2, 0, 0, 0, 0, loc),
	} {
		start := date.AddDate(0, 0, -7)
		hours := modelHours(start, int(date.Sub(start)/time.Hour)+1)
		if err := validateWarmup(hours, date); err != nil {
			t.Fatal(err)
		}
		hours[1].Precip = 1
		got := modelWetness(hours, Profile{Seepage: true})
		elapsed := date.Sub(hours[1].Time).Hours()
		if math.Abs(got[len(got)-1].wetness.seep-math.Exp2(-elapsed/seepHalfLife)) > 1e-12 {
			t.Fatal("DST must use elapsed drying hours")
		}
		if validateWarmup(hours[24:], date) == nil || validateWarmup(append(hours[:100:100], hours[101:]...), date) == nil {
			t.Fatal("accepted incomplete warmup")
		}
	}
}

func TestOptionalRadiationAndHumidity(t *testing.T) {
	for _, mode := range []string{"absent", "null", "present", "ragged", "negative"} {
		row := weatherRow([]int64{0, 3600}, 55)
		hourly := row["hourly"].(map[string]any)
		switch mode {
		case "null":
			hourly["shortwave_radiation"] = []any{nil, nil}
		case "present":
			hourly["shortwave_radiation"] = []float64{0, 400}
		case "ragged":
			hourly["shortwave_radiation"] = []float64{0}
		case "negative":
			hourly["shortwave_radiation"] = []float64{0, -1}
		}
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var raw omResponse
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		hours, err := toHours(raw, time.UTC)
		if mode == "ragged" || mode == "negative" {
			if err == nil {
				t.Fatal("accepted invalid radiation", mode)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if hours[1].Humidity != nil {
			t.Fatal("invented missing RH")
		}
		if mode == "present" && (hours[1].Radiation == nil || *hours[1].Radiation != 400) {
			t.Fatal("radiation lost")
		}
		// Dew point remains sufficient for drying with no RH field.
		hours[1].Precip = .1
		if modelWetness(hours, Profile{})[1].wetness.surface != 1 {
			t.Fatal("missing RH blocked model")
		}
	}
}

func TestAlternatingMoistureExplainsCombinedRating(t *testing.T) {
	w := steadyWindow(Window{Valid: true, TmaxF: 65})
	for i := range w.hours {
		if i < 2 {
			w.hours[i].wetness.surface = .3
		}
		if i >= 4 {
			w.hours[i].wetness.seep = .3
		}
	}
	p, th := Profile{Aspect: "shade", Seepage: true}, DefaultThresholds()
	fields := strings.Split(areaWindowData(w, p, "AM", th), ",")
	if fields[0] != "warn" || fields[3] != "seepage possible" || fields[18] != "Seepage possible" || fields[19] != "go|warn" {
		t.Fatal("combined rating lost its evidence", fields)
	}
	day := cragDays([]DayForecast{{AM: w, PM: w}}, p, th)[0]
	if day.Wetness != day.Reason || day.Wetness != "seepage possible" {
		t.Fatal("summary contradicts rating", day)
	}
}

// Recorded model weather is a reproducible integration fixture, not an
// observation of whether Little Si was climbable or a target forecast color.
func TestExitsRecordedWeather(t *testing.T) {
	data, err := os.ReadFile("testdata/exits-2026-10-29.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw omResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	hours, err := toHours(raw, loc)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, loc)
	if err := validateWarmup(hours, now); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Profile{
		{Aspect: "shade", DryingFactor: DryoutFor("rhyolite")},
		{Aspect: "shade", DryingFactor: DryoutFor("conglomerate"), Seepage: true},
	} {
		modeled := modelWetness(hours, p)
		days := Aggregate(modeled, loc, now, 7)
		th := DefaultThresholds()
		for _, day := range days {
			for i, w := range []Window{day.AM, day.PM} {
				win := []string{"AM", "PM"}[i]
				e := Explain(w, p, win, th)
				fields := strings.Split(areaWindowData(w, p, win, th), ",")
				if !w.Valid || len(fields) != 20 || fields[0] != classOf(e.Verdict) || fields[14] != explanationSummary(e) || fields[16] != "go|go" {
					t.Fatal("recorded forecast has inconsistent details", fields)
				}
				if m := reasonMetric(e.Reason); m == MetricPastRain || m == MetricSeepage {
					if fields[3] != e.Reason || iconOf(e.Result, w.CloudMean) != "i-drop" {
						t.Fatal("recorded moisture warning lost reason or icon", fields)
					}
				}
			}
		}
	}
}
