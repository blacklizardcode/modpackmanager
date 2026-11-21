package core

import (
	"fmt"
	"os"
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

	// stamp identifies the file as this document last read or wrote it, to
	// notice changes made by other programs (an AI assistant using the MCP
	// server, the command line, a second window).
	stamp fileStamp
}

type fileStamp struct {
	info os.FileInfo // nil when the file couldn't be read
}

func stampOf(path string) fileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	// Windows looks a file's identity up lazily, by path, when it's first
	// compared. Do it now, while the path still names this file.
	os.SameFile(st, st)
	return fileStamp{info: st}
}

// differs reports whether the file changed between two stamps. Programs that
// save safely (like this one) replace the file, which shows up even when
// the size and a coarse modification time stay the same.
func (s fileStamp) differs(now fileStamp) bool {
	a, b := s.info, now.info
	return a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) || !os.SameFile(a, b)
}

// Open loads any supported file. Imported (non-.mpm) packs get no Path so the
// first save asks where to store the .mpm file.
func Open(path string) (*Document, error) {
	stamp := stampOf(path) // before reading: a change during the read shows up later
	p, kind, err := formats.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	d := &Document{Pack: p, Kind: kind}
	if kind == formats.KindMPM {
		d.Path, d.stamp = path, stamp
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
	d.stamp = stampOf(path)
	return nil
}

// ChangedOnDisk reports whether another program changed or replaced the
// pack's file since this document last read or wrote it.
func (d *Document) ChangedOnDisk() bool {
	if d.Path == "" || d.stamp.info == nil {
		return false
	}
	now := stampOf(d.Path)
	return now.info != nil && d.stamp.differs(now)
}

// Reload reads the pack from its file again, dropping unsaved changes.
func (d *Document) Reload() error {
	nd, err := Open(d.Path)
	if err != nil {
		return err
	}
	d.Pack, d.Kind, d.Dirty, d.stamp = nd.Pack, nd.Kind, false, nd.stamp
	return nil
}

// AcknowledgeDiskChange accepts the file as it is now without reloading it:
// ChangedOnDisk reports only later changes, and saving replaces the file.
func (d *Document) AcknowledgeDiskChange() {
	if d.Path != "" {
		d.stamp = stampOf(d.Path)
	}
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
