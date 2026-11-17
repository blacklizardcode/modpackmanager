package imgutil

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func jpg(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func TestNormalizeScales(t *testing.T) {
	out, raw := Normalize(jpg(400, 200), 64)
	if raw {
		t.Fatal("jpeg should decode")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
		t.Fatalf("size %v", img.Bounds())
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	if out, raw := Normalize(svg, 64); !raw || !bytes.Equal(out, svg) {
		t.Fatal("svg should pass through")
	}
}

func TestLoaderCaches(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(jpg(10, 10))
	}))
	defer srv.Close()
	l := &Loader{HTTP: srv.Client(), CacheDir: t.TempDir(), mem: map[string][]byte{}}
	for i := 0; i < 3; i++ {
		if _, raw, err := l.Load(context.Background(), srv.URL+"/a.jpg", 32); err != nil || raw {
			t.Fatal(err, raw)
		}
	}
	l.mem = map[string][]byte{} // disk cache must serve the rest
	l.Load(context.Background(), srv.URL+"/a.jpg", 16)
	if hits != 1 {
		t.Fatalf("hits %d", hits)
	}
	if _, _, err := l.Load(context.Background(), "file:///etc/passwd", 0); err == nil {
		t.Fatal("file URL accepted")
	}
}
