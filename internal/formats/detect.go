// Package formats reads and writes modpack archives: Modrinth .mrpack,
// CurseForge .zip and the app's own .mpm format.
package formats

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"

	"modpackmanager/internal/model"
)

// Kind is a modpack file format.
type Kind string

const (
	KindMrpack     Kind = "mrpack"
	KindCurseForge Kind = "curseforge"
	KindMPM        Kind = "mpm"
)

func (k Kind) Label() string {
	switch k {
	case KindMrpack:
		return "Modrinth (.mrpack)"
	case KindCurseForge:
		return "CurseForge (.zip)"
	case KindMPM:
		return "Modpack Manager (.mpm)"
	}
	return string(k)
}

// Detect inspects the archive contents (not the extension).
func Detect(zr *zip.Reader) (Kind, error) {
	switch {
	case findFile(zr, mpmIndexName) != nil:
		return KindMPM, nil
	case findFile(zr, "modrinth.index.json") != nil:
		return KindMrpack, nil
	case findFile(zr, "manifest.json") != nil:
		return KindCurseForge, nil
	}
	return "", fmt.Errorf("unrecognised modpack: expected modrinth.index.json, manifest.json or %s", mpmIndexName)
}

// Read parses an archive of any supported kind.
func Read(data []byte) (*model.Pack, Kind, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("not a zip archive: %w", err)
	}
	kind, err := Detect(zr)
	if err != nil {
		return nil, "", err
	}
	var p *model.Pack
	switch kind {
	case KindMPM:
		p, err = ReadMPM(zr)
	case KindMrpack:
		p, err = ReadMrpack(zr)
	case KindCurseForge:
		p, err = ReadCurseForge(zr)
	}
	if err != nil {
		return nil, kind, err
	}
	if p.Name == "" {
		p.Name = "Untitled modpack"
	}
	return p, kind, nil
}

// ReadFile opens a modpack from disk.
func ReadFile(path string) (*model.Pack, Kind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return Read(data)
}
