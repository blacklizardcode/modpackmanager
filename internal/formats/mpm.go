package formats

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"modpackmanager/internal/model"
)

// MPMExtension is the file extension of the app's own format.
const MPMExtension = ".mpm"

// MPMFormatVersion is the newest format version this build reads. A pack is
// written with the oldest version that can hold it: 1, or 2 when it stores
// jar files (files/), so that older builds refuse such a pack instead of
// dropping the jars when they save it.
const MPMFormatVersion = 2

const mpmIndexName = "modpackmanager.json"

// mpmDoc is the JSON document stored inside an .mpm archive.
//
// An .mpm file is a zip containing:
//
//	modpackmanager.json   pack metadata, groups and mod entries (this struct)
//	files/<sha1>/<name>   jars the pack stores itself (format version 2)
//	overrides/...         files copied to client and server
//	client-overrides/...  files copied to the client only
//	server-overrides/...  files copied to the server only
type mpmDoc struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"formatVersion"`
	*model.Pack
}

// ReadMPM parses an .mpm archive.
func ReadMPM(zr *zip.Reader) (*model.Pack, error) {
	f := findFile(zr, mpmIndexName)
	if f == nil {
		return nil, fmt.Errorf("not a modpackmanager file: %s missing", mpmIndexName)
	}
	doc := mpmDoc{Pack: &model.Pack{}}
	if err := readJSON(f, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", mpmIndexName, err)
	}
	if doc.Format != "modpackmanager" {
		return nil, fmt.Errorf("unknown format %q", doc.Format)
	}
	if doc.FormatVersion > MPMFormatVersion {
		return nil, fmt.Errorf("file was made by a newer version of Modpack Manager (format %d)", doc.FormatVersion)
	}
	p := doc.Pack
	for _, m := range p.Mods {
		if m.ID == "" {
			m.ID = model.NewID()
		}
		if m.Side == "" {
			m.Side = model.SideUnknown
		}
		if m.File.Path != "" {
			if err := SafeRelPath(m.File.Path); err != nil {
				return nil, fmt.Errorf("mod %q: %w", m.DisplayName(), err)
			}
		}
	}
	stored, err := readStoredFiles(zr)
	if err != nil {
		return nil, err
	}
	for _, m := range p.Mods {
		if data, ok := stored[strings.ToLower(m.File.SHA1)]; ok && m.File.SHA1 != "" {
			m.File.Data = data
		}
	}
	ov, err := readOverrides(zr, map[string]model.Side{
		"overrides/":        model.SideBoth,
		"client-overrides/": model.SideClient,
		"server-overrides/": model.SideServer,
	})
	if err != nil {
		return nil, err
	}
	p.Overrides = ov
	promoteJars(p)
	return p, nil
}

// WriteMPM serialises the full pack.
func WriteMPM(w io.Writer, p *model.Pack) error {
	zw := zip.NewWriter(w)
	if p.Groups == nil {
		p.Groups = []*model.Group{}
	}
	if p.Mods == nil {
		p.Mods = []*model.Mod{}
	}
	version := 1
	for _, m := range p.Mods {
		if m.File.Stored() {
			if err := checkStored(m); err != nil {
				return err
			}
			version = 2
		}
	}
	data, err := json.MarshalIndent(mpmDoc{Format: "modpackmanager", FormatVersion: version, Pack: p}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipFile(zw, mpmIndexName, data); err != nil {
		return err
	}
	written := map[string]bool{}
	for _, m := range p.Mods {
		if !m.File.Stored() || written[strings.ToLower(m.File.SHA1)] {
			continue
		}
		written[strings.ToLower(m.File.SHA1)] = true
		if err := writeZipFile(zw, storedFileName(m), m.File.Data); err != nil {
			return err
		}
	}
	for _, o := range p.Overrides {
		if err := writeZipFile(zw, overridePrefix(o.Side)+o.Path, o.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// SaveMPM writes the pack atomically to path.
func SaveMPM(path string, p *model.Pack) error {
	var buf bytes.Buffer
	if err := WriteMPM(&buf, p); err != nil {
		return err
	}
	return WriteFileAtomic(path, buf.Bytes())
}

// WriteFileAtomic writes via a temp file + rename so a crash never leaves a
// half-written file behind.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mpm-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// CreateTemp makes owner-only files; keep the existing file's mode, or use
	// the usual 0644 for a new one.
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
