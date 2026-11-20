package core

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

// Target selects which part of the pack to export.
type Target string

const (
	TargetClient Target = "client" // client-only mods
	TargetCommon Target = "common" // mods needed on both sides
	TargetServer Target = "server" // server-only mods
	TargetFull   Target = "full"   // everything
)

func (t Target) Label() string {
	switch t {
	case TargetClient:
		return "Client"
	case TargetCommon:
		return "Common"
	case TargetServer:
		return "Server"
	}
	return "Full pack"
}

// Format is an export file format.
type Format string

const (
	FormatMrpack     Format = "mrpack"
	FormatCurseForge Format = "curseforge"
	FormatZip        Format = "zip" // ready-to-run folder layout with jars downloaded
)

func (f Format) Label() string {
	switch f {
	case FormatMrpack:
		return "Modrinth pack (.mrpack)"
	case FormatCurseForge:
		return "CurseForge pack (.zip)"
	case FormatZip:
		return "Plain folder zip with downloaded jars"
	}
	return string(f)
}

// Ext returns the file extension for the format.
func (f Format) Ext() string {
	if f == FormatMrpack {
		return ".mrpack"
	}
	return ".zip"
}

// ExportOptions configures Export.
type ExportOptions struct {
	Target Target
	Format Format
	// IncludeCommon adds the common (client+server) mods and overrides to a
	// client or server export, producing a complete, installable pack.
	IncludeCommon bool
	// EmbedNonModrinth ships jars that can't be referenced by a Modrinth CDN
	// URL inside the .mrpack overrides instead of linking to CurseForge.
	EmbedNonModrinth bool
}

// DefaultExportOptions are the defaults each export button starts with.
func DefaultExportOptions(t Target) ExportOptions {
	o := ExportOptions{Target: t, Format: FormatMrpack}
	if t == TargetServer {
		// A server can't start without the common mods.
		o.IncludeCommon = true
		o.Format = FormatZip
	}
	return o
}

// SelectMods returns the mods an export includes.
func SelectMods(p *model.Pack, t Target, includeCommon bool) []*model.Mod {
	var out []*model.Mod
	for _, m := range p.Mods {
		side := m.EffectiveSide()
		ok := false
		switch t {
		case TargetFull:
			ok = true
		case TargetClient:
			ok = side == model.SideClient || (includeCommon && side == model.SideBoth)
		case TargetServer:
			ok = side == model.SideServer || (includeCommon && side == model.SideBoth)
		case TargetCommon:
			ok = side == model.SideBoth
		}
		if ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].DisplayName()) < strings.ToLower(out[j].DisplayName())
	})
	return out
}

// SelectOverrides returns the override files an export includes.
func SelectOverrides(p *model.Pack, t Target, includeCommon bool) []*model.OverrideFile {
	var out []*model.OverrideFile
	for _, o := range p.Overrides {
		ok := false
		switch t {
		case TargetFull:
			ok = true
		case TargetClient:
			ok = o.Side == model.SideClient || (includeCommon && o.Side == model.SideBoth)
		case TargetServer:
			ok = o.Side == model.SideServer || (includeCommon && o.Side == model.SideBoth)
		case TargetCommon:
			ok = o.Side == model.SideBoth
		}
		if ok {
			out = append(out, o)
		}
	}
	return out
}

// CountStored returns how many of the mods are jars the pack stores itself;
// exports bundle those.
func CountStored(mods []*model.Mod) int {
	n := 0
	for _, m := range mods {
		if m.File.Stored() {
			n++
		}
	}
	return n
}

// ExportResult summarises an export.
type ExportResult struct {
	Mods     int
	Embedded int
	Skipped  []string
	Report
}

// ModrinthAllowedPrefixes are the download hosts modrinth.com accepts in packs.
var ModrinthAllowedPrefixes = []string{"https://cdn.modrinth.com/", "https://github.com/", "https://raw.githubusercontent.com/", "https://gitlab.com/"}

// isModrinthURL reports whether a URL is on a host the Modrinth platform
// allows in .mrpack files.
func isModrinthURL(u string) bool {
	l := strings.ToLower(u)
	for _, h := range ModrinthAllowedPrefixes {
		if strings.HasPrefix(l, h) {
			return true
		}
	}
	return false
}

// Export writes the selected part of the pack and returns the archive bytes.
func (s *Service) Export(ctx context.Context, p *model.Pack, opt ExportOptions, dl *Downloader, prog Progress) ([]byte, *ExportResult, error) {
	if dl == nil {
		dl = NewDownloader()
	}
	mods := SelectMods(p, opt.Target, opt.IncludeCommon)
	ovs := SelectOverrides(p, opt.Target, opt.IncludeCommon)
	res := &ExportResult{}
	if opt.Target == TargetServer {
		for _, m := range mods {
			if m.Side == model.SideUnknown || m.Side == "" {
				res.Warn("%s has no side information and was included as a common mod. Set its side if it is client-only.", m.DisplayName())
			}
		}
	}

	// Work out which jars must be downloaded.
	need := map[string]bool{}
	for _, m := range mods {
		switch opt.Format {
		case FormatZip:
			need[m.ID] = true
		case FormatCurseForge:
			if m.CurseForge == nil || m.CurseForge.FileID == 0 {
				need[m.ID] = true
			}
		case FormatMrpack:
			hasMR := false
			for _, u := range m.File.URLs {
				if isModrinthURL(u) {
					hasMR = true
				}
			}
			complete := m.File.SHA1 != "" && m.File.SHA512 != "" && len(m.File.URLs) > 0
			if !complete || (!hasMR && opt.EmbedNonModrinth) {
				need[m.ID] = true
			}
		}
	}
	data := map[string][]byte{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	done := 0
	total := len(need)
	prog.report("Downloading mods", 0, total)
	for _, m := range mods {
		if !need[m.ID] {
			continue
		}
		wg.Add(1)
		go func(m *model.Mod) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := dl.Fetch(ctx, m.File)
			mu.Lock()
			defer mu.Unlock()
			done++
			prog.report("Downloading "+m.DisplayName(), done, total)
			if err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", m.DisplayName(), err))
				return
			}
			data[m.ID] = b
		}(m)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(res.Skipped)

	// Drop mods we could not obtain where the format needs the bytes.
	var final []*model.Mod
	embedded := map[string][]byte{}
	for _, m := range mods {
		b, have := data[m.ID]
		switch opt.Format {
		case FormatZip, FormatCurseForge:
			if need[m.ID] && !have {
				continue
			}
			if need[m.ID] {
				embedded[m.ID] = b
			}
		case FormatMrpack:
			if need[m.ID] {
				if !have {
					continue
				}
				f := m.File
				FillHashes(&f, b)
				hasMR := false
				for _, u := range f.URLs {
					if isModrinthURL(u) {
						hasMR = true
					}
				}
				// A file without a link (a jar the pack stores) can only
				// travel inside the overrides.
				if len(f.URLs) == 0 || (opt.EmbedNonModrinth && !hasMR) {
					embedded[m.ID] = b
				} else {
					cp := *m
					cp.File = f
					m = &cp
				}
			} else if len(m.File.URLs) > 0 && !isModrinthURL(m.File.URLs[0]) {
				res.Warn("%s is downloaded from %s; Modrinth's website rejects packs with non-Modrinth links, but launchers such as Prism accept them.", m.DisplayName(), hostOf(m.File.URLs[0]))
			}
		}
		final = append(final, m)
	}
	res.Mods = len(final)
	res.Embedded = len(embedded)
	prog.report("Writing archive", total, total)

	var buf bytes.Buffer
	var err error
	switch opt.Format {
	case FormatMrpack:
		err = formats.WriteMrpack(&buf, p, final, ovs, formats.MrpackWriteOptions{Embedded: embedded})
	case FormatCurseForge:
		err = formats.WriteCurseForge(&buf, p, final, flattenOverrides(ovs), formats.CurseForgeWriteOptions{Embedded: embedded})
	case FormatZip:
		err = writeFolderZip(&buf, final, embedded, flattenOverrides(ovs))
	default:
		err = fmt.Errorf("unknown format %q", opt.Format)
	}
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), res, nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		return u[:i]
	}
	return u
}

// flattenOverrides merges side-specific overrides into one set; side-specific
// files win over common ones with the same path.
func flattenOverrides(ovs []*model.OverrideFile) []*model.OverrideFile {
	byPath := map[string]*model.OverrideFile{}
	var order []string
	for _, o := range ovs {
		prev, ok := byPath[o.Path]
		if !ok {
			order = append(order, o.Path)
		}
		if !ok || prev.Side == model.SideBoth {
			cp := *o
			cp.Side = model.SideBoth
			byPath[o.Path] = &cp
		}
	}
	out := make([]*model.OverrideFile, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

func writeFolderZip(buf *bytes.Buffer, mods []*model.Mod, jars map[string][]byte, ovs []*model.OverrideFile) error {
	zw := zip.NewWriter(buf)
	now := time.Now()
	written := map[string]bool{}
	for _, o := range ovs {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: o.Path, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		if _, err := w.Write(o.Data); err != nil {
			return err
		}
		written[o.Path] = true
	}
	for _, m := range mods {
		b, ok := jars[m.ID]
		if !ok || written[m.File.Path] {
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: m.File.Path, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return zw.Close()
}

// SuggestedFileName builds "<Name>-<version>-<target>.<ext>".
func SuggestedFileName(p *model.Pack, opt ExportOptions) string {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if strings.ContainsRune(`/\:*?"<>|`, r) {
				return '_'
			}
			return r
		}, strings.TrimSpace(s))
		return strings.ReplaceAll(s, " ", "-")
	}
	name := clean(p.Name)
	if name == "" {
		name = "modpack"
	}
	if v := clean(p.Version); v != "" {
		name += "-" + v
	}
	if opt.Target != TargetFull {
		name += "-" + string(opt.Target)
	}
	return name + opt.Format.Ext()
}
