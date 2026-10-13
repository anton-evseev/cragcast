package deploycloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testClient(s *httptest.Server) client {
	return client{http: s.Client(), baseURL: s.URL, wait: func(context.Context, time.Duration) error { return nil }}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "seattle"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"index.html": "hello", "about.html": "hello", "seattle/crag.html": "<p>Forecast 🌤</p>"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestManifest(t *testing.T) {
	root := fixture(t)
	manifest, assets, err := readAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 3 || len(assets) != 2 {
		t.Fatalf("manifest=%v assets=%d", manifest, len(assets))
	}
	if manifest["/index.html"] != (manifestEntry{Hash: "5deeda9d2277e0ed541bfc03adafb3cf", Size: 5}) {
		t.Fatal(manifest)
	}
	if manifest["/about.html"] != manifest["/index.html"] {
		t.Fatal("duplicate contents must share a hash")
	}
	if _, ok := manifest["/seattle/crag.html"]; !ok {
		t.Fatal("missing nested URL path")
	}
	if err := os.WriteFile(filepath.Join(root, "unknown.extension"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, assets, err = readAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := assets[manifest["/unknown.extension"].Hash].contentType; got != "text/plain; charset=utf-8" {
		t.Fatal(got)
	}
}

func TestInvalidBundle(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.html")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root + "/missing", file, t.TempDir()} {
		if _, _, err := readAssets(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if err := os.Symlink(file, filepath.Join(root, "link.html")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAssets(root); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, _, err := readAssets(filepath.Join(root, "link.html")); err == nil {
		t.Fatal("accepted symlink root")
	}
	if os.Geteuid() != 0 {
		root := t.TempDir()
		file := filepath.Join(root, "unreadable.html")
		if err := os.WriteFile(file, []byte("hello"), 0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readAssets(root); err == nil {
			t.Fatal("accepted unreadable file")
		}
	}
}

func TestUploadFlow(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple buckets", true: "unchanged assets"}[unchanged], func(t *testing.T) {
			root := fixture(t)
			var hashes []string
			contents := make(map[string]string)
			var requests int
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				workerPath := "/accounts/" + account + "/workers/scripts/test-worker"
				switch {
				case r.Method == "POST" && r.URL.Path == workerPath+"/assets-upload-session":
					if r.Header.Get("Authorization") != "Bearer api-secret" {
						t.Error("wrong session authentication")
					}
					var body struct {
						Manifest map[string]manifestEntry `json:"manifest"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if len(body.Manifest) != 3 {
						t.Error(body.Manifest)
					}
					for path, entry := range body.Manifest {
						data, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(path, "/")))
						if err != nil {
							t.Error(err)
							return
						}
						if entry.Size != len(data) {
							t.Error("wrong file size")
						}
						contents[entry.Hash] = string(data)
					}
					for hash := range contents {
						hashes = append(hashes, hash)
					}
					slices.Sort(hashes)
					buckets := [][]string{{hashes[0]}, {hashes[1]}}
					token := "upload-secret"
					if unchanged {
						buckets = nil
						token = "completion-secret"
					}
					json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"jwt": token, "buckets": buckets}})
				case r.Method == "POST" && r.URL.Path == "/accounts/"+account+"/workers/assets/upload":
					if unchanged {
						t.Error("uploaded unchanged files")
					}
					if r.Header.Get("Authorization") != "Bearer upload-secret" || r.URL.Query().Get("base64") != "true" {
						t.Error("wrong upload authentication/query")
					}
					reader, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					part, err := reader.NextPart()
					if err != nil {
						t.Error(err)
						return
					}
					hash := hashes[requests-2]
					if part.FormName() != hash || part.Header.Get("Content-Type") != "text/html; charset=utf-8" {
						t.Error("wrong multipart metadata")
					}
					data, err := io.ReadAll(part)
					if err != nil {
						t.Error(err)
						return
					}
					decoded, err := base64.StdEncoding.DecodeString(string(data))
					if err != nil || string(decoded) != contents[hash] {
						t.Error("wrong uploaded contents")
					}
					if _, err := reader.NextPart(); err != io.EOF {
						t.Error("unexpected part")
					}
					token := ""
					if requests == 3 {
						token = "completion-secret"
						w.WriteHeader(201)
					}
					json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"jwt": token}})
				case r.Method == "PUT" && r.URL.Path == workerPath:
					if r.Header.Get("Authorization") != "Bearer api-secret" {
						t.Error("wrong deployment authentication")
					}
					reader, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					part, err := reader.NextPart()
					if err != nil {
						t.Error(err)
						return
					}
					if part.FormName() != "metadata" || part.Header.Get("Content-Type") != "application/json" {
						t.Error("wrong deployment part")
					}
					var metadata map[string]any
					if err := json.NewDecoder(part).Decode(&metadata); err != nil {
						t.Error(err)
						return
					}
					if metadata["compatibility_date"] != "2026-10-06" || len(metadata) != 2 {
						t.Error(metadata)
					}
					assets := metadata["assets"].(map[string]any)
					if assets["jwt"] != "completion-secret" {
						t.Error("wrong completion token")
					}
					config := assets["config"].(map[string]any)
					if config["html_handling"] != "auto-trailing-slash" || config["not_found_handling"] != "404-page" {
						t.Error(config)
					}
					if _, err := reader.NextPart(); err != io.EOF {
						t.Error("assets-only deployment included extra parts")
					}
					io.WriteString(w, `{"success":true,"result":{"id":"test-worker"}}`)
				default:
					t.Error("unexpected request", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer s.Close()
			err := testClient(s).run(context.Background(), Options{From: root, Worker: "test-worker", AccountID: account, APIToken: "api-secret"})
			if err != nil {
				t.Fatal(err)
			}
			want := 4
			if unchanged {
				want = 2
			}
			if requests != want {
				t.Fatalf("requests=%d, want %d", requests, want)
			}
		})
	}
}

func TestInvalidSessionAndCompletion(t *testing.T) {
	for _, tc := range []struct{ name, session, upload, want string }{
		{"missing upload token", `{"buckets":[]}`, "", "upload token"},
		{"invalid result", `null`, "", "upload token"},
		{"unknown hash", `{"jwt":"secret","buckets":[["unknown"]]}`, "", "unknown file hash"},
		{"empty bucket", `{"jwt":"secret","buckets":[[]]}`, "", "empty upload bucket"},
		{"missing completion", `{"jwt":"secret","buckets":[["5deeda9d2277e0ed541bfc03adafb3cf"]]}`, `{}`, "completion token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					t.Error("deployed after invalid upload")
					w.WriteHeader(400)
					return
				}
				result := tc.session
				if strings.HasSuffix(r.URL.Path, "/upload") {
					result = tc.upload
				}
				io.WriteString(w, `{"success":true,"result":`+result+`}`)
			}))
			defer s.Close()
			err := testClient(s).run(context.Background(), Options{From: fixture(t), Worker: "test-worker", AccountID: account, APIToken: "api-secret"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestRequestFailuresAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		retry    bool
		attempts int
		want     string
	}{
		{"rate limited", 429, `{"success":false}`, true, 3, "HTTP 429"},
		{"server unavailable", 503, `{"success":false}`, true, 3, "HTTP 503"},
		{"unauthorized", 401, `{"success":false,"errors":[{"code":1000,"message":"bad api-secret upload-secret completion-secret"}]}`, true, 1, "[redacted]"},
		{"API failure", 200, `{"success":false,"errors":[{"message":"rejected"}]}`, true, 1, "rejected"},
		{"malformed response", 200, `not JSON`, true, 1, "invalid JSON"},
		{"ambiguous deployment", 503, `{"success":false}`, false, 1, "outcome is unknown"},
		{"malformed deployment response", 200, `not JSON`, false, 1, "outcome is unknown"},
		{"missing deployment status", 200, `{}`, false, 1, "outcome is unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c := testClient(s)
			var delays []time.Duration
			c.wait = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
			_, err := c.request(context.Background(), "stage", "POST", "/", "api-secret", "application/json", []byte(`{}`), tc.retry, []string{"api-secret", "upload-secret", "completion-secret"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
			for _, token := range []string{"api-secret", "upload-secret", "completion-secret"} {
				if strings.Contains(err.Error(), token) {
					t.Fatal("token leaked")
				}
			}
			if int(count.Load()) != tc.attempts {
				t.Fatal(count.Load())
			}
			if tc.attempts == 3 && !slices.Equal(delays, []time.Duration{time.Second, 2 * time.Second}) {
				t.Fatal(delays)
			}
		})
	}
}

func TestTransientRecoveryAndNetworkFailure(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"success":true,"result":{"jwt":"token"}}`)
	}))
	c := testClient(s)
	if _, err := c.request(context.Background(), "upload", "POST", "/", "token", "application/json", nil, true, nil); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
	s.Close()
	// A dead server exercises transport failures without reaching Cloudflare.
	for _, retry := range []bool{true, false} {
		waits := 0
		c.wait = func(context.Context, time.Duration) error { waits++; return nil }
		_, err := c.request(context.Background(), "stage", "POST", "/", "token", "application/json", nil, retry, nil)
		if err == nil {
			t.Fatal("accepted transport failure")
		}
		if retry && waits != 2 || !retry && (waits != 0 || !strings.Contains(err.Error(), "outcome is unknown")) {
			t.Fatal(waits, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx, time.Hour); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestMultipartBucket(t *testing.T) {
	contents := map[string]asset{
		"hash1": {content: []byte("<p>hello</p>"), contentType: "text/html"},
		"hash2": {content: []byte{0, 255, 128, 10}, contentType: "application/octet-stream"},
	}
	body, contentType, err := uploadBody([]string{"hash1", "hash2"}, contents)
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for _, name := range []string{"hash1", "hash2"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() != name || part.Header.Get("Content-Type") != contents[name].contentType || !bytes.Equal(decoded, contents[name].content) {
			t.Fatal("invalid multipart file", name)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatal(err)
	}
}
