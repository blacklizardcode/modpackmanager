// Package imgutil downloads, caches and normalises images (mod icons,
// description pictures) into PNG bytes that every toolkit can display.
// WebP, PNG, JPEG and GIF are decoded in Go so no system codec is needed;
// anything else (SVG badges) is returned as-is for the toolkit to try.
package imgutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"modpackmanager/internal/api/httpx"
	"modpackmanager/internal/config"
)

// Loader fetches images with a disk cache and limited concurrency.
type Loader struct {
	HTTP     *http.Client
	CacheDir string
	sem      chan struct{}
	mu       sync.Mutex
	mem      map[string][]byte
}

// NewLoader uses the user cache directory.
func NewLoader() *Loader {
	return &Loader{HTTP: httpx.DefaultClient, CacheDir: filepath.Join(config.CacheDir(), "images"), sem: make(chan struct{}, 6), mem: map[string][]byte{}}
}

// MaxImageBytes caps downloads.
const MaxImageBytes = 15 << 20

// Load returns image bytes scaled so neither side exceeds maxSide (0 = no
// scaling). Raster images come back as PNG; others unchanged (raw=true).
func (l *Loader) Load(ctx context.Context, url string, maxSide int) (data []byte, raw bool, err error) {
	key := fmt.Sprintf("%d|%s", maxSide, url)
	l.mu.Lock()
	if b, ok := l.mem[key]; ok {
		l.mu.Unlock()
		return b, !isPNG(b), nil
	}
	l.mu.Unlock()
	if l.sem != nil {
		select {
		case l.sem <- struct{}{}:
			defer func() { <-l.sem }()
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	src, err := l.fetch(ctx, url)
	if err != nil {
		return nil, false, err
	}
	out, raw := Normalize(src, maxSide)
	l.mu.Lock()
	if len(l.mem) > 500 {
		l.mem = map[string][]byte{}
	}
	l.mem[key] = out
	l.mu.Unlock()
	return out, raw, nil
}

func isPNG(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG")) }

func (l *Loader) fetch(ctx context.Context, url string) ([]byte, error) {
	sum := sha256.Sum256([]byte(url))
	path := filepath.Join(l.CacheDir, hex.EncodeToString(sum[:16]))
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return b, nil
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, fmt.Errorf("unsupported image URL %q", url)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent)
	c := l.HTTP
	if c == nil {
		c = httpx.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &httpx.StatusError{Code: resp.StatusCode, URL: url}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxImageBytes {
		return nil, fmt.Errorf("image too large")
	}
	if l.CacheDir != "" && os.MkdirAll(l.CacheDir, 0o755) == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
	return b, nil
}

// Normalize decodes raster formats and re-encodes as (scaled) PNG.
func Normalize(src []byte, maxSide int) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return src, true
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src, true
	}
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		if w >= h {
			h = max(1, h*maxSide/w)
			w = maxSide
		} else {
			w = max(1, w*maxSide/h)
			h = maxSide
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		return src, true
	}
	return out.Bytes(), false
}
