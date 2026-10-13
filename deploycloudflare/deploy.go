// Package deploycloudflare publishes static assets to a Cloudflare Worker.
package deploycloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	apiURL            = "https://api.cloudflare.com/client/v4"
	compatibilityDate = "2026-10-06"
)

type Options struct {
	From      string
	Worker    string
	AccountID string
	APIToken  string
}

type manifestEntry struct {
	Hash string `json:"hash"`
	Size int    `json:"size"`
}

type asset struct {
	content     []byte
	contentType string
}

type envelope struct {
	Success *bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

type client struct {
	http    *http.Client
	baseURL string
}

// Run publishes the directory as an assets-only Worker, using built-in HTML
// and 404 routing settings. It does not change routes or custom domains.
func Run(ctx context.Context, opts Options) error {
	c := client{
		http:    &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: apiURL,
	}
	return c.run(ctx, opts)
}

func (c client) run(ctx context.Context, opts Options) error {
	if opts.From == "" {
		return fmt.Errorf("deploy-cloudflare: --from is required")
	}
	if !validName(opts.Worker) {
		return fmt.Errorf("deploy-cloudflare: invalid Worker name")
	}
	if len(opts.AccountID) != 32 {
		return fmt.Errorf("deploy-cloudflare: CLOUDFLARE_ACCOUNT_ID must be a 32-character hexadecimal account ID")
	}
	if _, err := hex.DecodeString(opts.AccountID); err != nil {
		return fmt.Errorf("deploy-cloudflare: invalid CLOUDFLARE_ACCOUNT_ID")
	}
	if strings.TrimSpace(opts.APIToken) == "" {
		return fmt.Errorf("deploy-cloudflare: CLOUDFLARE_API_TOKEN is required")
	}

	manifest, assets, err := readAssets(opts.From)
	if err != nil {
		return fmt.Errorf("deploy-cloudflare: %w", err)
	}
	log.Printf("Registering %d assets for Worker %s", len(manifest), opts.Worker)
	body, err := json.Marshal(struct {
		Manifest map[string]manifestEntry `json:"manifest"`
	}{manifest})
	if err != nil {
		return err
	}
	workerPath := "/accounts/" + opts.AccountID + "/workers/scripts/" + opts.Worker
	result, err := c.request(ctx, "register manifest", http.MethodPost, workerPath+"/assets-upload-session", opts.APIToken, "application/json", body, true, []string{opts.APIToken})
	if err != nil {
		return err
	}
	var session struct {
		JWT     string     `json:"jwt"`
		Buckets [][]string `json:"buckets"`
	}
	if err := json.Unmarshal(result, &session); err != nil || session.JWT == "" {
		return fmt.Errorf("register manifest: missing or invalid upload token")
	}
	// Validate all buckets before starting any file uploads.
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			return fmt.Errorf("register manifest: empty upload bucket")
		}
		for _, hash := range bucket {
			if _, ok := assets[hash]; !ok {
				return fmt.Errorf("register manifest: requested an unknown file hash")
			}
		}
	}
	secrets := []string{opts.APIToken, session.JWT}
	completion := session.JWT
	if len(session.Buckets) > 0 {
		completion = ""
	}
	for i, bucket := range session.Buckets {
		log.Printf("Uploading asset bucket %d/%d (%d files)", i+1, len(session.Buckets), len(bucket))
		body, contentType, err := uploadBody(bucket, assets)
		if err != nil {
			return fmt.Errorf("upload assets: %w", err)
		}
		result, err := c.request(ctx, "upload assets", http.MethodPost, "/accounts/"+opts.AccountID+"/workers/assets/upload?base64=true", session.JWT, contentType, body, true, secrets)
		if err != nil {
			return err
		}
		var upload struct {
			JWT string `json:"jwt"`
		}
		if err := json.Unmarshal(result, &upload); err != nil {
			return fmt.Errorf("upload assets: invalid result")
		}
		if upload.JWT != "" {
			completion = upload.JWT
		}
	}
	if completion == "" {
		return fmt.Errorf("upload assets: missing completion token")
	}
	secrets = append(secrets, completion)
	metadata := struct {
		CompatibilityDate string `json:"compatibility_date"`
		Assets            struct {
			JWT    string `json:"jwt"`
			Config struct {
				HTMLHandling     string `json:"html_handling"`
				NotFoundHandling string `json:"not_found_handling"`
			} `json:"config"`
		} `json:"assets"`
	}{CompatibilityDate: compatibilityDate}
	metadata.Assets.JWT = completion
	metadata.Assets.Config.HTMLHandling = "auto-trailing-slash"
	metadata.Assets.Config.NotFoundHandling = "404-page"
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var deployment bytes.Buffer
	form := multipart.NewWriter(&deployment)
	part, err := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="metadata"`}, "Content-Type": {"application/json"}})
	if err != nil {
		return err
	}
	if _, err := part.Write(encoded); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	log.Printf("Deploying Worker %s", opts.Worker)
	if _, err := c.request(ctx, "deploy Worker", http.MethodPut, workerPath, opts.APIToken, form.FormDataContentType(), deployment.Bytes(), false, secrets); err != nil {
		return err
	}
	log.Printf("Deployed Worker %s successfully", opts.Worker)
	return nil
}

func readAssets(root string) (map[string]manifestEntry, map[string]asset, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("bundle path must be a directory")
	}
	manifest := make(map[string]manifestEntry)
	assets := make(map[string]asset)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported file: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		ext := filepath.Ext(path)
		// Match the SHA-256 recipe in Cloudflare's direct-upload example.
		sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(content) + strings.TrimPrefix(ext, ".")))
		hash := hex.EncodeToString(sum[:16])
		manifest["/"+filepath.ToSlash(relative)] = manifestEntry{Hash: hash, Size: len(content)}
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = http.DetectContentType(content)
		}
		assets[hash] = asset{content: content, contentType: contentType}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(manifest) == 0 {
		return nil, nil, fmt.Errorf("bundle directory is empty")
	}
	return manifest, assets, nil
}

func uploadBody(bucket []string, assets map[string]asset) ([]byte, string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, hash := range bucket {
		a := assets[hash]
		part, err := form.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s"`, hash, hash)},
			"Content-Type":        {a.contentType},
		})
		if err != nil {
			return nil, "", err
		}
		encoded := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := encoded.Write(a.content); err != nil {
			return nil, "", err
		}
		if err := encoded.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := form.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), form.FormDataContentType(), nil
}

func (c client) request(ctx context.Context, stage, method, path, token, contentType string, body []byte, retry bool, secrets []string) (json.RawMessage, error) {
	attempts := 1
	if retry {
		attempts = 3
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := wait(ctx, time.Duration(attempt)*time.Second); err != nil {
				return nil, fmt.Errorf("%s: %w", stage, err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: invalid request", stage)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", contentType)
		resp, err := c.http.Do(req)
		if err != nil {
			if retry && ctx.Err() == nil && attempt+1 < attempts {
				continue
			}
			return nil, requestError(stage, redact(err.Error(), secrets), !retry)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
		resp.Body.Close()
		if retry && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt+1 < attempts {
			continue
		}
		if readErr != nil {
			if retry && ctx.Err() == nil && attempt+1 < attempts {
				continue
			}
			return nil, requestError(stage, "could not read response", !retry)
		}
		if len(data) > 4*1024*1024 {
			return nil, requestError(stage, "response too large", !retry)
		}
		var response envelope
		decodeErr := json.Unmarshal(data, &response)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := fmt.Sprintf("HTTP %d", resp.StatusCode)
			if decodeErr == nil {
				for _, e := range response.Errors {
					message += fmt.Sprintf("; Cloudflare %d: %s", e.Code, redact(e.Message, secrets))
				}
			}
			return nil, requestError(stage, message, !retry && resp.StatusCode >= 500)
		}
		if decodeErr != nil {
			return nil, requestError(stage, "invalid JSON response", !retry)
		}
		if response.Success == nil {
			return nil, requestError(stage, "missing API success status", !retry)
		}
		if !*response.Success {
			message := "Cloudflare reported failure"
			for _, e := range response.Errors {
				message += fmt.Sprintf("; %d: %s", e.Code, redact(e.Message, secrets))
			}
			return nil, requestError(stage, message, false)
		}
		return response.Result, nil
	}
	panic("unreachable")
}

func requestError(stage, message string, unknown bool) error {
	if unknown {
		message += "; deployment outcome is unknown; check the Worker before retrying"
	}
	return fmt.Errorf("%s: %s", stage, message)
}

func redact(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
