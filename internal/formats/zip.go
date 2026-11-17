package formats

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"modpackmanager/internal/model"
)

// MaxOverrideFileSize guards against zip bombs (per file, uncompressed).
const MaxOverrideFileSize = 512 << 20

// SafeRelPath rejects absolute paths, Windows drive paths and ".." escapes.
func SafeRelPath(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if strings.ContainsRune(p, '\\') {
		p = strings.ReplaceAll(p, "\\", "/")
	}
	if strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		return errors.New("absolute path not allowed")
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("path escapes the instance folder")
	}
	return nil
}

func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func readJSON(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(v)
}

func readAll(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > MaxOverrideFileSize {
		return nil, fmt.Errorf("%s is too large (%d bytes)", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxOverrideFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxOverrideFileSize {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	return data, nil
}

// readOverrides collects all files under the given prefixes.
func readOverrides(zr *zip.Reader, prefixes map[string]model.Side) ([]*model.OverrideFile, error) {
	var out []*model.OverrideFile
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		for prefix, side := range prefixes {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rel := strings.TrimPrefix(name, prefix)
			if rel == "" {
				continue
			}
			if err := SafeRelPath(rel); err != nil {
				return nil, fmt.Errorf("override %q: %w", name, err)
			}
			data, err := readAll(f)
			if err != nil {
				return nil, err
			}
			out = append(out, &model.OverrideFile{Side: side, Path: rel, Data: data})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Side != out[j].Side {
			return out[i].Side < out[j].Side
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func overridePrefix(s model.Side) string {
	switch s {
	case model.SideClient:
		return "client-overrides/"
	case model.SideServer:
		return "server-overrides/"
	}
	return "overrides/"
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
