package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hour is one hourly observation/forecast point in the crag's local time.
type Hour struct {
	Time    time.Time
	TempF   float64
	Precip  float64 // inches accumulated in the preceding hour
	WindMph float64
	DewF    float64
	Cloud   float64 // cloud cover, percent 0–100
}

// Forecaster fetches hourly weather for a coordinate. The interface exists so
// tests can inject a fake returning canned data or a forced error.
type Forecaster interface {
	Fetch(ctx context.Context, lat, lon float64, loc *time.Location) ([]Hour, error)
}

// OpenMeteo is the real Forecaster, hitting api.open-meteo.com.
type OpenMeteo struct {
	Client  *http.Client
	BaseURL string // defaults to the public endpoint
}

func NewOpenMeteo() *OpenMeteo {
	return &OpenMeteo{
		Client:  &http.Client{Timeout: 20 * time.Second},
		BaseURL: "https://api.open-meteo.com/v1/forecast",
	}
}

// raw mirrors the Open-Meteo JSON response (only the fields we request).
type omResponse struct {
	Hourly struct {
		Time       []string  `json:"time"`
		Temp       []float64 `json:"temperature_2m"`
		Precip     []float64 `json:"precipitation"`
		Wind       []float64 `json:"wind_speed_10m"`
		DewPoint   []float64 `json:"dew_point_2m"`
		CloudCover []float64 `json:"cloud_cover"`
	} `json:"hourly"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func (o *OpenMeteo) Fetch(ctx context.Context, lat, lon float64, loc *time.Location) ([]Hour, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", lat))
	q.Set("longitude", fmt.Sprintf("%.4f", lon))
	q.Set("hourly", strings.Join([]string{
		"temperature_2m", "precipitation", "wind_speed_10m", "dew_point_2m", "cloud_cover",
	}, ","))
	q.Set("temperature_unit", "fahrenheit")
	q.Set("wind_speed_unit", "mph")
	q.Set("precipitation_unit", "inch")
	q.Set("timezone", loc.String()) // daily roll-up + "today" align to crag-local time
	q.Set("past_days", "3")         // antecedent precip window
	q.Set("forecast_days", "7")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "cragcast (https://github.com/you/cragcast)")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo status %d", resp.StatusCode)
	}

	var raw omResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if raw.Error {
		return nil, fmt.Errorf("open-meteo error: %s", raw.Reason)
	}
	return toHours(raw, loc)
}

// toHours zips the parallel arrays into []Hour, parsing local timestamps.
func toHours(raw omResponse, loc *time.Location) ([]Hour, error) {
	n := len(raw.Hourly.Time)
	// Guard against ragged arrays so an index never panics.
	if len(raw.Hourly.Temp) != n || len(raw.Hourly.Precip) != n ||
		len(raw.Hourly.Wind) != n || len(raw.Hourly.DewPoint) != n ||
		len(raw.Hourly.CloudCover) != n {
		return nil, fmt.Errorf("open-meteo arrays misaligned (time=%d)", n)
	}
	hours := make([]Hour, 0, n)
	for i := 0; i < n; i++ {
		// Open-Meteo emits local time without an offset, e.g. "2026-06-13T14:00".
		t, err := time.ParseInLocation("2006-01-02T15:04", raw.Hourly.Time[i], loc)
		if err != nil {
			return nil, fmt.Errorf("parse time %q: %w", raw.Hourly.Time[i], err)
		}
		hours = append(hours, Hour{
			Time:    t,
			TempF:   raw.Hourly.Temp[i],
			Precip:  raw.Hourly.Precip[i],
			WindMph: raw.Hourly.Wind[i],
			DewF:    raw.Hourly.DewPoint[i],
			Cloud:   raw.Hourly.CloudCover[i],
		})
	}
	return hours, nil
}
