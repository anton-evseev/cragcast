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
	Valid      bool
	TmaxF      float64
	DewMaxF    float64
	WindMaxMph float64
	PrecipIn   float64 // total rain in the window
	CloudMean  float64 // drives the solar-gain term
	Antecedent float64 // total rain in the prior 72h up to window start
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

// window aggregates the hours falling in [date+startH, date+endH) and computes
// the antecedent precip leading up to the window's start.
func window(hours []Hour, date time.Time, startH, endH int) Window {
	start := date.Add(time.Duration(startH) * time.Hour)
	end := date.Add(time.Duration(endH) * time.Hour)

	w := Window{Antecedent: precipSum(hours, start.Add(-antecedentHrs*time.Hour), start)}
	var cloudSum float64
	var count int
	for _, h := range hours {
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
		w.PrecipIn += h.Precip
		cloudSum += h.Cloud
		count++
	}
	if count > 0 {
		w.CloudMean = cloudSum / float64(count)
	}
	return w
}

// precipSum totals precipitation over [from, to). Because past_days and the
// forecast come back as one continuous array, today's lookback reaches into the
// past portion of the same slice — no separate history store needed.
func precipSum(hours []Hour, from, to time.Time) float64 {
	var sum float64
	for _, h := range hours {
		if h.Time.Before(from) || !h.Time.Before(to) {
			continue
		}
		sum += h.Precip
	}
	return sum
}
