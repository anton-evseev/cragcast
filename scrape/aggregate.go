package scrape

import "time"

// Climbing-day window boundaries, local hours.
const (
	amStart, amEnd = 7, 13  // 7am–1pm
	pmStart, pmEnd = 13, 19 // 1pm–7pm
	antecedentHrs  = 72     // lookback for "since rain"
)

// Window is one day-half, aggregated. Each variable uses the operation that
// preserves the signal the verdict cares about (max temp, summed precip, etc.).
type Window struct {
	AQI               *float64
	Valid             bool
	TmaxF             float64
	DewMaxF           float64
	WindMaxMph        float64
	PrecipIn          float64  // total rain in the window
	PrecipProbability *float64 // peak hourly chance, only with complete window coverage
	CloudMean         float64  // drives the solar-gain term
	Antecedent        float64  // total rain in the prior 72h up to window start
}

// DayForecast is one calendar day split into its two climbing windows.
type DayForecast struct {
	Date time.Time
	AM   Window
	PM   Window
}

// Aggregate turns the continuous hourly series into `days` DayForecasts,
// starting from `now`'s local date. `now` should already be in `loc`.
func Aggregate(hours []Hour, loc *time.Location, now time.Time, days int) []DayForecast {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	out := make([]DayForecast, 0, days)
	for d := 0; d < days; d++ {
		date := today.AddDate(0, 0, d)
		out = append(out, DayForecast{
			Date: date,
			AM:   window(hours, date, amStart, amEnd),
			PM:   window(hours, date, pmStart, pmEnd),
		})
	}
	return out
}

// window aggregates the hours between the local calendar boundaries and computes
// the antecedent precip leading up to the window's start.
func window(hours []Hour, date time.Time, startH, endH int) Window {
	start := time.Date(date.Year(), date.Month(), date.Day(), startH, 0, 0, 0, date.Location())
	end := time.Date(date.Year(), date.Month(), date.Day(), endH, 0, 0, 0, date.Location())

	w := Window{Antecedent: precipSum(hours, start.Add(-antecedentHrs*time.Hour), start)}
	var cloudSum float64
	var count int
	probabilities := map[int64]float64{}
	coverage := map[int64]int{}
	for _, h := range hours {
		coverage[h.Time.Unix()]++
		// Precipitation and probability describe the hour ending at Time.
		if h.Time.After(start) && !h.Time.After(end) {
			w.PrecipIn += h.Precip
			if h.PrecipProbability != nil {
				probabilities[h.Time.Unix()] = *h.PrecipProbability
			}
		}
		if h.Time.Before(start) || !h.Time.Before(end) {
			continue
		}
		if !w.Valid { // first hour in window — seed the maxes
			w.Valid = true
			w.TmaxF, w.DewMaxF, w.WindMaxMph = h.TempF, h.DewF, h.WindMph
		}
		w.TmaxF = max(w.TmaxF, h.TempF)
		w.DewMaxF = max(w.DewMaxF, h.DewF)
		w.WindMaxMph = max(w.WindMaxMph, h.WindMph)
		w.AQI = peakAQI(w.AQI, h.AQI)
		cloudSum += h.Cloud
		count++
	}
	if count > 0 {
		w.CloudMean = cloudSum / float64(count)
	}
	peak := 0.0
	complete := true
	for t := start.Add(time.Hour); !t.After(end); t = t.Add(time.Hour) {
		p, ok := probabilities[t.Unix()]
		if !ok {
			complete = false
			break
		}
		peak = max(peak, p)
	}
	if complete {
		w.PrecipProbability = &peak
	}
	// Require every hour used by the window and its rain history exactly once.
	// Rain describes the preceding hour, so the lookback start is excluded.
	for t := start.Add(-antecedentHrs * time.Hour).Add(time.Hour); !t.After(end); t = t.Add(time.Hour) {
		if coverage[t.Unix()] != 1 {
			w.Valid = false
			break
		}
	}
	return w
}

// precipSum totals precipitation whose hour ends in (from, to]. Because past_days and the
// forecast come back as one continuous array, today's lookback reaches into the
// past portion of the same slice — no separate history store needed.
func precipSum(hours []Hour, from, to time.Time) float64 {
	var sum float64
	for _, h := range hours {
		if !h.Time.After(from) || h.Time.After(to) {
			continue
		}
		sum += h.Precip
	}
	return sum
}

// peakAQI preserves missing values and takes the maximum of available values.
func peakAQI(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil || *a >= *b {
		return a
	}
	return b
}
