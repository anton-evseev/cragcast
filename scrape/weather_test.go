package scrape

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anton-evseev/cragcast/config"
)

func TestRequiredWeatherValues(t *testing.T) {
	for _, field := range []string{"time", "temperature_2m", "precipitation", "wind_speed_10m", "dew_point_2m", "cloud_cover"} {
		t.Run(field, func(t *testing.T) {
			row := weatherRow([]int64{0, 3600}, 0)
			row["hourly"].(map[string]any)[field] = []any{0, nil}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var raw omResponse
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			if _, err := toHours(raw, time.UTC); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("expected missing %s error, got %v", field, err)
			}
		})
	}
	// Zero is a real measurement, including a Unix timestamp of zero.
	data, err := json.Marshal(weatherRow([]int64{0, 3600}, 0))
	if err != nil {
		t.Fatal(err)
	}
	var raw omResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	hours, err := toHours(raw, time.UTC)
	if err != nil || len(hours) != 2 || hours[0].TempF != 0 || hours[0].Precip != 0 {
		t.Fatalf("zero measurements rejected: %v %v", hours, err)
	}
}

func TestWeatherTimestampCoverage(t *testing.T) {
	for _, times := range [][]int64{{0, 7200}, {0, 0}, {3600, 0}} {
		data, err := json.Marshal(weatherRow(times, 65))
		if err != nil {
			t.Fatal(err)
		}
		var raw omResponse
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, err := toHours(raw, time.UTC); err == nil {
			t.Fatalf("accepted invalid timestamps: %v", times)
		}
	}
}

func TestWindowWeatherCoverage(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{
		time.Date(2026, 10, 14, 0, 0, 0, 0, loc),
		time.Date(2026, 3, 8, 0, 0, 0, 0, loc),
		time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
	} {
		t.Run(date.Format("2006-01-02"), func(t *testing.T) {
			// Use calendar hours across daylight-saving transitions.
			start := time.Date(date.Year(), date.Month(), date.Day(), 7, 0, 0, 0, loc)
			end := time.Date(date.Year(), date.Month(), date.Day(), 19, 0, 0, 0, loc)
			var hours []Hour
			for ts := start.Add(-72 * time.Hour).Add(time.Hour); !ts.After(end); ts = ts.Add(time.Hour) {
				hours = append(hours, Hour{Time: ts, TempF: 65})
			}
			day := Aggregate(hours, loc, date, 1)[0]
			if !day.AM.Valid || !day.PM.Valid {
				t.Fatal("complete weather rejected, including optional missing probability and AQI")
			}
			for _, missing := range []time.Time{hours[0].Time, start, start.Add(time.Hour), start.Add(6 * time.Hour), end} {
				partial := slices.DeleteFunc(slices.Clone(hours), func(h Hour) bool { return h.Time.Equal(missing) })
				day := Aggregate(partial, loc, date, 1)[0]
				if day.AM.Valid && day.PM.Valid {
					t.Fatalf("missing hour %s accepted", missing)
				}
				w := day.AM
				if w.Valid {
					w = day.PM
				}
				if got := Score(w, Profile{}, "AM", DefaultThresholds()); got.Reason != "no data" {
					t.Fatalf("incomplete window scored: %+v", got)
				}
			}
			duplicate := append(slices.Clone(hours), hours[1])
			if Aggregate(duplicate, loc, date, 1)[0].AM.Valid {
				t.Fatal("duplicate history hour accepted")
			}
		})
	}
}

type incompleteWeather struct{}

func (incompleteWeather) FetchBatch(_ context.Context, points []Coordinate, _ *time.Location) ([][]Hour, error) {
	rows := make([][]Hour, len(points))
	for i := range rows {
		rows[i] = []Hour{{Time: time.Now().Truncate(time.Hour), TempF: 65}}
	}
	return rows, nil
}

func TestIncompleteWeatherPreservesPages(t *testing.T) {
	dir := t.TempDir()
	regionPath := filepath.Join(dir, "test.html")
	if err := os.WriteFile(regionPath, []byte("previous forecast"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := scraper{fc: incompleteWeather{}, th: DefaultThresholds(), dataDir: dir}
	r := config.Region{Id: "test", Timezone: "UTC", Areas: []config.Area{{Id: "area", Crags: []config.Crag{{Lat: 47, Lon: -122}}}}}
	if err := s.region(r); err == nil || !strings.Contains(err.Error(), "incomplete weather coverage") {
		t.Fatalf("expected coverage error, got %v", err)
	}
	data, err := os.ReadFile(regionPath)
	if err != nil || string(data) != "previous forecast" {
		t.Fatalf("previous page changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed scrape wrote files: %v %v", entries, err)
	}
}
