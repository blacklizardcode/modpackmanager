package core

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"modpackmanager/internal/api/httpx"
	"modpackmanager/internal/config"
	"modpackmanager/internal/model"
)

// Downloader fetches mod files with hash verification and an on-disk cache.
type Downloader struct {
	HTTP     *http.Client
	CacheDir string
}

func NewDownloader() *Downloader {
	return &Downloader{HTTP: httpx.DefaultClient, CacheDir: filepath.Join(config.CacheDir(), "files")}
}

// Fetch returns the bytes of a mod's file, trying each URL in turn.
func (d *Downloader) Fetch(ctx context.Context, f model.File) ([]byte, error) {
	if f.SHA1 != "" {
		if data, err := os.ReadFile(d.cachePath(f.SHA1)); err == nil && verify(data, f) == nil {
			return data, nil
		}
	}
	if len(f.URLs) == 0 {
		return nil, fmt.Errorf("%s has no download URL", f.Name())
	}
	var lastErr error
	for _, u := range f.URLs {
		data, err := d.get(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		if err := verify(data, f); err != nil {
			lastErr = fmt.Errorf("%s: %w", u, err)
			continue
		}
		if d.CacheDir != "" {
			sum := sha1.Sum(data)
			if err := os.MkdirAll(d.CacheDir, 0o755); err == nil {
				_ = os.WriteFile(d.cachePath(hex.EncodeToString(sum[:])), data, 0o644)
			}
		}
		return data, nil
	}
	return nil, lastErr
}

func (d *Downloader) cachePath(sha string) string {
	return filepath.Join(d.CacheDir, strings.ToLower(sha))
}

func (d *Downloader) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent)
	c := d.HTTP
	if c == nil {
		c = httpx.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &httpx.StatusError{Code: resp.StatusCode, URL: u}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<30))
}

func verify(data []byte, f model.File) error {
	if f.SHA1 != "" {
		s := sha1.Sum(data)
		if !strings.EqualFold(hex.EncodeToString(s[:]), f.SHA1) {
			return fmt.Errorf("sha1 mismatch")
		}
	}
	if f.SHA512 != "" {
		s := sha512.Sum512(data)
		if !strings.EqualFold(hex.EncodeToString(s[:]), f.SHA512) {
			return fmt.Errorf("sha512 mismatch")
		}
	}
	if f.Size > 0 && int64(len(data)) != f.Size {
		return fmt.Errorf("size mismatch (%d != %d)", len(data), f.Size)
	}
	return nil
}

// FillHashes computes missing hashes from file bytes.
func FillHashes(f *model.File, data []byte) {
	if f.SHA1 == "" {
		s := sha1.Sum(data)
		f.SHA1 = hex.EncodeToString(s[:])
	}
	if f.SHA512 == "" {
		s := sha512.Sum512(data)
		f.SHA512 = hex.EncodeToString(s[:])
	}
	if f.Size == 0 {
		f.Size = int64(len(data))
	}
}
