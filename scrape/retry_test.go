package scrape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestOpenMeteoHTTPRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		failures   int32
		retryAfter string
		body       string
		calls      int32
		elapsed    time.Duration
		wantError  bool
	}{
		{"503 recovery", 503, 2, "", `{}`, 3, 6 * time.Second, false},
		{"408 recovery", 408, 1, "", `{}`, 2, 2 * time.Second, false},
		{"429 retry-after seconds", 429, 1, "7", `{}`, 2, 7 * time.Second, false},
		{"502 exhausted", 502, 4, "", `{}`, 4, 14 * time.Second, true},
		{"504 recovery", 504, 1, "", `{}`, 2, 2 * time.Second, false},
		{"400 permanent", 400, 4, "", `{}`, 1, 0, true},
		{"403 permanent", 403, 4, "", `{}`, 1, 0, true},
		{"invalid JSON", 200, 0, "", `invalid`, 1, 0, true},
		{"wrong location count", 200, 0, "", `[]`, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) <= tc.failures {
						w.Header().Set("Retry-After", tc.retryAfter)
						w.WriteHeader(tc.status)
						return
					}
					fmt.Fprint(w, tc.body)
				}))
				start := time.Now()
				_, err := requestBatch[map[string]any](context.Background(), server.Client(), server.URL, nil, 1)
				if (err != nil) != tc.wantError || calls.Load() != tc.calls || time.Since(start) != tc.elapsed {
					t.Fatalf("calls=%d elapsed=%s err=%v", calls.Load(), time.Since(start), err)
				}
			})
		})
	}
}

func TestOpenMeteoTransportRetries(t *testing.T) {
	for _, mode := range []string{"timeout", "header timeout", "connection lost", "truncated body"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						switch mode {
						case "header timeout":
							<-r.Context().Done()
						case "timeout":
							w.WriteHeader(200)
							http.NewResponseController(w).Flush()
							<-r.Context().Done()
						case "connection lost":
							conn, _, err := http.NewResponseController(w).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							conn.Close()
						case "truncated body":
							fmt.Fprint(w, `{"hourly":`)
						}
						return
					}
					fmt.Fprint(w, `{}`)
				}))
				client := server.Client()
				client.Timeout = NewOpenMeteo().Client.Timeout
				start := time.Now()
				_, err := requestBatch[map[string]any](context.Background(), client, server.URL, nil, 1)
				elapsed := 2 * time.Second
				if mode == "timeout" || mode == "header timeout" {
					elapsed += 60 * time.Second
				}
				if err != nil || calls.Load() != 2 || time.Since(start) != elapsed {
					t.Fatalf("calls=%d elapsed=%s err=%v", calls.Load(), time.Since(start), err)
				}
			})
		})
	}
}

func TestOpenMeteoRetryCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(503)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		start := time.Now()
		_, err := requestJSON(ctx, server.Client(), server.URL, nil)
		if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 2 || time.Since(start) != 3*time.Second {
			t.Fatalf("calls=%d elapsed=%s err=%v", calls.Load(), time.Since(start), err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		_, err = requestJSON(ctx, server.Client(), server.URL, nil)
		if !errors.Is(err, context.Canceled) || calls.Load() != 2 {
			t.Fatalf("canceled request ran: %v", err)
		}
	})
}

func TestRetryAfterDate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			header string
			want   time.Duration
		}{
			{time.Now().Add(9 * time.Second).UTC().Format(http.TimeFormat), 9 * time.Second},
			{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), 2 * time.Second},
			{"1", 2 * time.Second}, {"garbage", 2 * time.Second}, {"-10", 2 * time.Second},
		} {
			if got := retryDelay(tc.header, 2*time.Second); got != tc.want {
				t.Fatalf("%q: got %s want %s", tc.header, got, tc.want)
			}
		}
	})
}

func TestOnlyFailedEndpointRetries(t *testing.T) {
	for _, endpoint := range []string{"/weather", "/air"} {
		t.Run(endpoint, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var weatherCalls, airCalls atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls := &weatherCalls
					if r.URL.Path == "/air" {
						calls = &airCalls
					}
					if calls.Add(1) == 1 && r.URL.Path == endpoint {
						w.WriteHeader(503)
						return
					}
					if r.URL.Path == "/air" {
						json.NewEncoder(w).Encode(map[string]any{"hourly": map[string]any{"time": []int64{0}, "us_aqi": []float64{125}}})
					} else {
						json.NewEncoder(w).Encode(weatherRow([]int64{0}, 65))
					}
				}))
				o := &OpenMeteo{Client: server.Client(), BaseURL: server.URL + "/weather", AirQualityURL: server.URL + "/air"}
				rows, err := o.FetchBatch(context.Background(), []Coordinate{{47, -122}}, time.UTC)
				if err != nil || len(rows) != 1 || rows[0][0].AQI == nil || *rows[0][0].AQI != 125 {
					t.Fatalf("rows=%v err=%v", rows, err)
				}
				wantWeather, wantAir := int32(1), int32(1)
				if endpoint == "/weather" {
					wantWeather = 2
				} else {
					wantAir = 2
				}
				if weatherCalls.Load() != wantWeather || airCalls.Load() != wantAir {
					t.Fatalf("weather=%d air=%d", weatherCalls.Load(), airCalls.Load())
				}
			})
		})
	}
}
