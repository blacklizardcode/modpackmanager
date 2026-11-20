// Package httpx contains the small HTTP helpers shared by the API clients.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"modpackmanager/internal/version"
)

// UserAgent identifies the app to the APIs (Modrinth requires a unique one).
var UserAgent = "modpackmanager/" + version.String() + " (desktop modpack editor; https://github.com/blacklizardcode/modpackmanager)"

// DefaultClient has sane timeouts.
var DefaultClient = &http.Client{Timeout: 60 * time.Second}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	Code int
	URL  string
	Body string
}

func (e *StatusError) Error() string {
	msg := e.Body
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Sprintf("HTTP %d from %s: %s", e.Code, e.URL, msg)
}

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// DoJSON performs a request, retrying on 429/5xx with backoff, and decodes the
// JSON response into out (if non-nil).
func DoJSON(ctx context.Context, c *http.Client, method, url string, headers map[string]string, body any, out any) error {
	if c == nil {
		c = DefaultClient
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			if !sleep(ctx, backoff(attempt, "")) {
				return ctx.Err()
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &StatusError{Code: resp.StatusCode, URL: url, Body: string(data)}
			if !sleep(ctx, backoff(attempt, resp.Header.Get("Retry-After"))) {
				return ctx.Err()
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &StatusError{Code: resp.StatusCode, URL: url, Body: string(data)}
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decoding %s: %w", url, err)
		}
		return nil
	}
	return lastErr
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 && s < 60 {
		return time.Duration(s)*time.Second + 100*time.Millisecond
	}
	return time.Duration(250*(1<<attempt)) * time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
