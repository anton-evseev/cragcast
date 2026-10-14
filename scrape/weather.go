package scrape

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userAgent = "CragCast (https://github.com/anton-evseev/cragcast)"

// Hour is one hourly observation/forecast point in the crag's local time.
type Hour struct {
	Time              time.Time
	TempF             float64
	Precip            float64  // inches accumulated in the preceding hour
	PrecipProbability *float64 // nil when precipitation probability is unavailable
	WindMph           float64
	DewF              float64
	AQI               *float64 // nil when air quality is unavailable
	Cloud             float64  // cloud cover, percent 0–100
}

// Forecaster fetches weather and optional air quality for a batch of coordinates.
type Forecaster interface {
	FetchBatch(ctx context.Context, points []Coordinate, loc *time.Location) ([][]Hour, error)
}

// OpenMeteo uses the separate weather and air quality endpoints.
type OpenMeteo struct {
	Client        *http.Client
	BaseURL       string // weather endpoint
	AirQualityURL string
}

func NewOpenMeteo() *OpenMeteo {
	return &OpenMeteo{
		Client:        &http.Client{Timeout: 20 * time.Second},
		BaseURL:       "https://api.open-meteo.com/v1/forecast",
		AirQualityURL: "https://air-quality-api.open-meteo.com/v1/air-quality",
	}
}

// raw mirrors the Open-Meteo JSON response (only the fields we request).
type omResponse struct {
	Hourly struct {
		Time              []*int64   `json:"time"`
		Temp              []*float64 `json:"temperature_2m"`
		Precip            []*float64 `json:"precipitation"`
		PrecipProbability []*float64 `json:"precipitation_probability"`
		Wind              []*float64 `json:"wind_speed_10m"`
		DewPoint          []*float64 `json:"dew_point_2m"`
		CloudCover        []*float64 `json:"cloud_cover"`
	} `json:"hourly"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

// Coordinate identifies a requested location, independent of the returned grid cell.
type Coordinate struct{ Lat, Lon float64 }

const batchSize = 32

// FetchBatch runs one weather request and one optional AQI request concurrently.
// Results retain the order of points; batches are scheduled by the scraper.
func (o *OpenMeteo) FetchBatch(ctx context.Context, points []Coordinate, loc *time.Location) ([][]Hour, error) {
	if len(points) == 0 {
		return nil, nil
	}
	if len(points) > batchSize {
		return nil, fmt.Errorf("batch exceeds %d locations", batchSize)
	}
	q := url.Values{}
	var lats, lons []string
	for _, p := range points {
		lats = append(lats, fmt.Sprintf("%.4f", p.Lat))
		lons = append(lons, fmt.Sprintf("%.4f", p.Lon))
	}
	q.Set("latitude", strings.Join(lats, ","))
	q.Set("longitude", strings.Join(lons, ","))
	q.Set("timezone", loc.String())
	q.Set("timeformat", "unixtime")
	q.Set("forecast_days", "7")
	aq := url.Values{}
	for key, values := range q {
		aq[key] = append([]string(nil), values...)
	}
	aq.Set("hourly", "us_aqi")
	q.Set("hourly", "temperature_2m,precipitation,precipitation_probability,wind_speed_10m,dew_point_2m,cloud_cover")
	q.Set("temperature_unit", "fahrenheit")
	q.Set("wind_speed_unit", "mph")
	q.Set("precipitation_unit", "inch")
	q.Set("past_days", "3")
	type airResult struct {
		data []aqResponse
		err  error
	}
	airDone := make(chan airResult, 1)
	go func() {
		data, err := requestBatch[aqResponse](ctx, o.Client, o.AirQualityURL, aq, len(points))
		if err == nil {
			for _, r := range data {
				if r.Error {
					err = fmt.Errorf("open-meteo error: %s", r.Reason)
					break
				}
				if len(r.Hourly.Time) == 0 || len(r.Hourly.Time) != len(r.Hourly.AQI) {
					err = fmt.Errorf("AQI arrays misaligned")
					break
				}
			}
		}
		airDone <- airResult{data, err}
	}()
	raw, err := requestBatch[omResponse](ctx, o.Client, o.BaseURL, q, len(points))
	air := <-airDone
	if err != nil {
		return nil, err
	}
	if air.err != nil {
		log.Printf("Warning: AQI unavailable for batch: %v", air.err)
	}
	out := make([][]Hour, len(points))
	for i, r := range raw {
		if r.Error {
			return nil, fmt.Errorf("open-meteo error: %s", r.Reason)
		}
		out[i], err = toHours(r, loc)
		if err != nil {
			return nil, fmt.Errorf("location %d: %w", i, err)
		}
		if air.err == nil {
			byTime := make(map[int64]*float64)
			for j, ts := range air.data[i].Hourly.Time {
				byTime[ts] = air.data[i].Hourly.AQI[j]
			}
			for j := range out[i] {
				out[i][j].AQI = byTime[out[i][j].Time.Unix()]
			}
		}
	}
	return out, nil
}

type aqResponse struct {
	Hourly struct {
		Time []int64    `json:"time"`
		AQI  []*float64 `json:"us_aqi"`
	} `json:"hourly"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func requestBatch[T any](ctx context.Context, client *http.Client, endpoint string, q url.Values, count int) ([]T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo status %d", resp.StatusCode)
	}
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var rows []T
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		err = json.Unmarshal(body, &rows)
	} else {
		var row T
		err = json.Unmarshal(body, &row)
		rows = []T{row}
	}
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(rows) != count {
		return nil, fmt.Errorf("open-meteo returned %d locations, expected %d", len(rows), count)
	}
	return rows, nil
}

// toHours zips the weather arrays into local hourly points.
func toHours(raw omResponse, loc *time.Location) ([]Hour, error) {
	n := len(raw.Hourly.Time)
	// Guard against ragged arrays so an index never panics.
	if n == 0 || len(raw.Hourly.Temp) != n || len(raw.Hourly.Precip) != n ||
		len(raw.Hourly.Wind) != n || len(raw.Hourly.DewPoint) != n ||
		len(raw.Hourly.CloudCover) != n {
		return nil, fmt.Errorf("open-meteo arrays misaligned (time=%d)", n)
	}
	if len(raw.Hourly.PrecipProbability) != 0 && len(raw.Hourly.PrecipProbability) != n {
		return nil, fmt.Errorf("open-meteo probability array misaligned")
	}
	hours := make([]Hour, 0, n)
	for i := 0; i < n; i++ {
		if raw.Hourly.Time[i] == nil {
			return nil, fmt.Errorf("open-meteo missing time at hour %d", i)
		}
		if i > 0 && *raw.Hourly.Time[i]-*raw.Hourly.Time[i-1] != int64(time.Hour/time.Second) {
			return nil, fmt.Errorf("open-meteo timestamps are not consecutive hours at hour %d", i)
		}
		for _, field := range []struct {
			name  string
			value *float64
		}{
			{"temperature_2m", raw.Hourly.Temp[i]},
			{"precipitation", raw.Hourly.Precip[i]},
			{"wind_speed_10m", raw.Hourly.Wind[i]},
			{"dew_point_2m", raw.Hourly.DewPoint[i]},
			{"cloud_cover", raw.Hourly.CloudCover[i]},
		} {
			if field.value == nil {
				return nil, fmt.Errorf("open-meteo missing %s at hour %d", field.name, i)
			}
		}
		var probability *float64
		if len(raw.Hourly.PrecipProbability) != 0 {
			probability = raw.Hourly.PrecipProbability[i]
			if probability != nil && (*probability < 0 || *probability > 100) {
				return nil, fmt.Errorf("open-meteo probability outside 0–100")
			}
		}
		t := time.Unix(*raw.Hourly.Time[i], 0).In(loc)
		hours = append(hours, Hour{
			Time:              t,
			TempF:             *raw.Hourly.Temp[i],
			Precip:            *raw.Hourly.Precip[i],
			PrecipProbability: probability,
			WindMph:           *raw.Hourly.Wind[i],
			DewF:              *raw.Hourly.DewPoint[i],
			Cloud:             *raw.Hourly.CloudCover[i],
		})
	}
	return hours, nil
}
