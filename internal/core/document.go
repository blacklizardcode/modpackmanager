package core

import (
	"fmt"
	"path/filepath"
	"strings"

	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

// Document is an open modpack plus where it came from.
type Document struct {
	Pack *model.Pack
	// Path is the .mpm file the pack is saved to ("" until first save).
	Path string
	// ImportedFrom is the original file for imported packs.
	ImportedFrom string
	Kind         formats.Kind
	Dirty        bool
}

// Open loads any supported file. Imported (non-.mpm) packs get no Path so the
// first save asks where to store the .mpm file.
func Open(path string) (*Document, error) {
	p, kind, err := formats.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	d := &Document{Pack: p, Kind: kind}
	if kind == formats.KindMPM {
		d.Path = path
	} else {
		d.ImportedFrom = path
		d.Dirty = true
	}
	return d, nil
}

// NewDocument starts an empty pack.
func NewDocument(name, mc, loader, loaderVersion string) *Document {
	p := model.New(name)
	if mc != "" {
		p.Minecraft = mc
	}
	p.Loader, p.LoaderVersion = loader, loaderVersion
	return &Document{Pack: p, Kind: formats.KindMPM, Dirty: true}
}

// NeedsEnrichment reports whether metadata should be fetched after opening.
func (d *Document) NeedsEnrichment() bool {
	if d.Kind != formats.KindMPM {
		return true
	}
	for _, m := range d.Pack.Mods {
		if m.Name == "" {
			return true
		}
	}
	return false
}

// Save writes the document to its path (or the given one).
func (d *Document) Save(path string) error {
	if path == "" {
		path = d.Path
	}
	if path == "" {
		return fmt.Errorf("no file name")
	}
	if !strings.HasSuffix(strings.ToLower(path), formats.MPMExtension) {
		path += formats.MPMExtension
	}
	if err := formats.SaveMPM(path, d.Pack); err != nil {
		return err
	}
	d.Path = path
	d.Kind = formats.KindMPM
	d.Dirty = false
	return nil
}

// Title for window headers.
func (d *Document) Title() string {
	name := d.Pack.Name
	if name == "" {
		name = "Untitled"
	}
	if d.Dirty {
		return "• " + name
	}
	return name
}

// SuggestedSavePath proposes an .mpm path next to the imported file.
func (d *Document) SuggestedSavePath() string {
	if d.Path != "" {
		return d.Path
	}
	base := d.Pack.Name
	if base == "" {
		base = "modpack"
	}
	base = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, base)
	dir := ""
	if d.ImportedFrom != "" {
		dir = filepath.Dir(d.ImportedFrom)
	}
	return filepath.Join(dir, base+formats.MPMExtension)
}
