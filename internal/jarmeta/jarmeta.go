// Package jarmeta reads the metadata that Minecraft mod loaders keep inside a
// mod jar, so that a jar added from disk gets a proper name, version and side.
//
// Supported files: fabric.mod.json (Fabric), quilt.mod.json (Quilt),
// META-INF/neoforge.mods.toml (NeoForge 1.20.5 and later), META-INF/mods.toml
// (Forge, and NeoForge up to 1.20.4) and mcmod.info (Forge 1.12 and older).
// Jars come from anywhere, so reading them never fails hard: whatever can't
// be parsed is simply left empty.
package jarmeta

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"modpackmanager/internal/model"
)

// Loader ids, as used by model.Pack.Loader.
const (
	Fabric   = "fabric"
	Quilt    = "quilt"
	Forge    = "forge"
	NeoForge = "neoforge"
)

// Info is what a jar says about itself. Unknown fields are empty.
type Info struct {
	ID          string // the loader's mod id, e.g. "sodium"
	Name        string
	Version     string
	Description string
	Authors     []string
	// Loaders the jar has metadata for. Empty for a jar without mod metadata
	// (a library, a server plugin, …).
	Loaders []string
	// Side comes from Fabric's or Quilt's "environment" field. Forge metadata
	// has no such field, so Forge mods are SideUnknown.
	Side model.Side
	// IconPath is the icon's path inside the jar ("" if none).
	IconPath string
}

// ErrNotJar is returned for data that isn't a zip archive.
var ErrNotJar = errors.New("isn't a valid .jar file (not a zip archive)")

// Limits for what is read from a jar.
const (
	maxEntrySize = 4 << 20 // a metadata file or an icon
	maxName      = 200
	maxVersion   = 100
	maxDesc      = 4000
)

// Read parses a jar's metadata. It fails only when data isn't a zip archive;
// a jar without readable mod metadata gives an Info without Loaders.
func Read(data []byte) (*Info, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotJar
	}
	return read(zr), nil
}

// Icon returns the raw bytes of the jar's icon, or nil if it has none.
func Icon(data []byte) []byte {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	info := read(zr)
	if info.IconPath == "" {
		return nil
	}
	return entry(zr, info.IconPath)
}

func read(zr *zip.Reader) *Info {
	info := &Info{Side: model.SideUnknown}
	// The first file found wins for each field, so the order is a priority.
	if b := entry(zr, "fabric.mod.json"); b != nil {
		info.addLoader(Fabric)
		parseFabric(b, info)
	}
	if b := entry(zr, "quilt.mod.json"); b != nil {
		info.addLoader(Quilt)
		parseQuilt(b, info)
	}
	if b := entry(zr, "META-INF/neoforge.mods.toml"); b != nil {
		info.addLoader(NeoForge)
		parseModsTOML(b, info, zr)
	}
	if b := entry(zr, "META-INF/mods.toml"); b != nil {
		info.addLoader(parseModsTOML(b, info, zr))
	}
	if b := entry(zr, "mcmod.info"); b != nil {
		info.addLoader(Forge)
		parseMcmodInfo(b, info)
	}
	return info
}

// entry reads one file from the jar, or returns nil.
func entry(zr *zip.Reader, name string) []byte {
	name = strings.TrimPrefix(name, "/")
	for _, f := range zr.File {
		if f.Name != name || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxEntrySize {
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxEntrySize+1))
		rc.Close()
		if err != nil || len(b) > maxEntrySize {
			return nil
		}
		return b
	}
	return nil
}

func (in *Info) addLoader(l string) {
	for _, x := range in.Loaders {
		if x == l {
			return
		}
	}
	in.Loaders = append(in.Loaders, l)
}

// fill sets the fields that are still empty.
func (in *Info) fill(id, name, version, desc string) {
	if in.ID == "" {
		in.ID = clean(id, maxName)
	}
	if in.Name == "" {
		in.Name = clean(name, maxName)
	}
	if in.Version == "" {
		in.Version = cleanVersion(version)
	}
	if in.Description == "" {
		in.Description = clean(desc, maxDesc)
	}
}

func (in *Info) addAuthor(a string) {
	a = clean(a, maxName)
	if a == "" {
		return
	}
	for _, x := range in.Authors {
		if strings.EqualFold(x, a) {
			return
		}
	}
	in.Authors = append(in.Authors, a)
}

func (in *Info) setSide(s model.Side) {
	if in.Side == model.SideUnknown {
		in.Side = s
	}
}

func (in *Info) setIcon(p string) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if in.IconPath == "" && p != "" {
		in.IconPath = p
	}
}

// clean drops Minecraft formatting codes (§a, §l, …) and control
// characters other than line breaks and tabs, trims, and caps the length.
func clean(s string, limit int) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '§':
			i++ // the code character that follows
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > limit {
		out = strings.TrimSpace(string([]rune(out)[:limit])) + "…"
	}
	return out
}

func cleanVersion(v string) string {
	v = clean(v, maxVersion)
	if strings.Contains(v, "${") {
		return "" // a build placeholder that was never filled in
	}
	return v
}

// ---------------------------------------------------------------------------
// Fabric and Quilt

func parseFabric(b []byte, in *Info) {
	var m struct {
		ID          json.RawMessage `json:"id"`
		Version     json.RawMessage `json:"version"`
		Name        json.RawMessage `json:"name"`
		Description json.RawMessage `json:"description"`
		Authors     json.RawMessage `json:"authors"`
		Environment json.RawMessage `json:"environment"`
		Icon        json.RawMessage `json:"icon"`
	}
	if json.Unmarshal(lenient(b), &m) != nil {
		return
	}
	in.fill(text(m.ID), text(m.Name), text(m.Version), text(m.Description))
	for _, a := range people(m.Authors) {
		in.addAuthor(a)
	}
	switch strings.ToLower(strings.TrimSpace(text(m.Environment))) {
	case "client":
		in.setSide(model.SideClient)
	case "server":
		in.setSide(model.SideServer)
	default: // "*", or missing: Fabric loads the mod everywhere
		in.setSide(model.SideBoth)
	}
	in.setIcon(icon(m.Icon))
}

func parseQuilt(b []byte, in *Info) {
	var q struct {
		Loader struct {
			ID       json.RawMessage `json:"id"`
			Version  json.RawMessage `json:"version"`
			Metadata struct {
				Name         json.RawMessage            `json:"name"`
				Description  json.RawMessage            `json:"description"`
				Contributors map[string]json.RawMessage `json:"contributors"`
				Icon         json.RawMessage            `json:"icon"`
			} `json:"metadata"`
		} `json:"quilt_loader"`
		Minecraft struct {
			Environment json.RawMessage `json:"environment"`
		} `json:"minecraft"`
	}
	if json.Unmarshal(lenient(b), &q) != nil {
		return
	}
	l := q.Loader
	in.fill(text(l.ID), text(l.Metadata.Name), text(l.Version), text(l.Metadata.Description))
	names := make([]string, 0, len(l.Metadata.Contributors))
	for name := range l.Metadata.Contributors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, n := range names {
		in.addAuthor(n)
	}
	switch strings.ToLower(strings.TrimSpace(text(q.Minecraft.Environment))) {
	case "client":
		in.setSide(model.SideClient)
	case "dedicated_server":
		in.setSide(model.SideServer)
	default:
		in.setSide(model.SideBoth)
	}
	in.setIcon(icon(l.Metadata.Icon))
}

// text returns a JSON string, or the literal text of a JSON number.
func text(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// people reads an author list: strings or {"name": …} objects, or one of those
// on its own.
func people(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		list = []json.RawMessage{raw}
	}
	var out []string
	for _, p := range list {
		if s := text(p); s != "" {
			out = append(out, s)
			continue
		}
		var o struct {
			Name json.RawMessage `json:"name"`
		}
		if json.Unmarshal(p, &o) == nil {
			if s := text(o.Name); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// icon reads an icon field: a path, or a map of sizes to paths (the largest
// wins).
func icon(raw json.RawMessage) string {
	if s := text(raw); s != "" {
		return s
	}
	var sizes map[string]string
	if len(raw) == 0 || json.Unmarshal(raw, &sizes) != nil {
		return ""
	}
	best, bestN := "", -1
	for k, v := range sizes {
		n, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil {
			n = 0
		}
		if v != "" && (n > bestN || (n == bestN && v < best)) {
			best, bestN = v, n
		}
	}
	return best
}

// lenient fixes two mistakes that are common in hand-written mod metadata
// and that Go's JSON decoder rejects: a byte order mark, and raw line breaks
// or tabs inside strings.
func lenient(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			case c == '\n':
				out = append(out, '\\', 'n')
				continue
			case c == '\t':
				out = append(out, '\\', 't')
				continue
			case c < 0x20:
				continue
			}
		} else if c == '"' {
			inStr = true
		}
		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Forge and NeoForge

// parseModsTOML reads META-INF/mods.toml or META-INF/neoforge.mods.toml and
// returns which loader the file is for: NeoForge when a dependency names
// "neoforge" (NeoForge used mods.toml up to 1.20.4), Forge otherwise.
func parseModsTOML(b []byte, in *Info, zr *zip.Reader) string {
	doc := parseTOML(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	loader := Forge
	for name, tables := range doc.arrays {
		if !strings.HasPrefix(name, "dependencies.") {
			continue
		}
		for _, t := range tables {
			if strings.EqualFold(strings.TrimSpace(t["modId"]), "neoforge") {
				loader = NeoForge
			}
		}
	}
	mods := doc.arrays["mods"]
	if len(mods) == 0 {
		return loader
	}
	m := mods[0]
	version := m["version"]
	if strings.Contains(version, "${file.jarVersion}") {
		jarVersion := manifestValue(entry(zr, "META-INF/MANIFEST.MF"), "Implementation-Version")
		version = strings.ReplaceAll(version, "${file.jarVersion}", jarVersion)
	}
	in.fill(m["modId"], m["displayName"], version, m["description"])
	in.addAuthor(m["authors"])
	logo := m["logoFile"]
	if logo == "" {
		logo = doc.top["logoFile"]
	}
	in.setIcon(logo)
	return loader
}

// manifestValue reads one attribute from a jar manifest (META-INF/MANIFEST.MF).
func manifestValue(b []byte, key string) string {
	var cur string
	match := func() string {
		k, v, ok := strings.Cut(cur, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		if strings.HasPrefix(line, " ") {
			cur += line[1:] // a continuation line
			continue
		}
		if v := match(); v != "" {
			return v
		}
		cur = line
	}
	return match()
}

func parseMcmodInfo(b []byte, in *Info) {
	b = lenient(b)
	var list []json.RawMessage
	if json.Unmarshal(b, &list) != nil {
		var v2 struct {
			ModList []json.RawMessage `json:"modList"`
		}
		if json.Unmarshal(b, &v2) != nil {
			return
		}
		list = v2.ModList
	}
	if len(list) == 0 {
		return
	}
	var e struct {
		ModID       json.RawMessage `json:"modid"`
		Name        json.RawMessage `json:"name"`
		Description json.RawMessage `json:"description"`
		Version     json.RawMessage `json:"version"`
		AuthorList  json.RawMessage `json:"authorList"`
		Authors     json.RawMessage `json:"authors"`
		LogoFile    json.RawMessage `json:"logoFile"`
	}
	if json.Unmarshal(list[0], &e) != nil {
		return
	}
	in.fill(text(e.ModID), text(e.Name), text(e.Version), text(e.Description))
	for _, a := range append(people(e.AuthorList), people(e.Authors)...) {
		in.addAuthor(a)
	}
	in.setIcon(text(e.LogoFile))
}
