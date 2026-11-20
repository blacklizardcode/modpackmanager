// Package model holds the in-memory representation of a modpack that every
// importer, exporter and UI works on.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

// Side says where a mod has to be installed.
type Side string

const (
	SideClient  Side = "client"  // only needed on the client
	SideServer  Side = "server"  // only needed on the server
	SideBoth    Side = "both"    // needed on client and server ("common")
	SideUnknown Side = "unknown" // no data; treated as common when exporting
)

// AllSides in display order.
var AllSides = []Side{SideBoth, SideClient, SideServer, SideUnknown}

func (s Side) Label() string {
	switch s {
	case SideClient:
		return "Client only"
	case SideServer:
		return "Server only"
	case SideBoth:
		return "Client & server"
	default:
		return "Unknown"
	}
}

// OnClient reports whether a mod with this side belongs in a client install.
func (s Side) OnClient() bool { return s != SideServer }

// OnServer reports whether a mod with this side belongs in a server install.
func (s Side) OnServer() bool { return s != SideClient }

// ParseSide accepts the canonical values plus a few friendly aliases.
func ParseSide(v string) Side {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "client", "client-only", "clientonly", "c":
		return SideClient
	case "server", "server-only", "serveronly", "s":
		return SideServer
	case "both", "common", "client&server", "client+server", "b":
		return SideBoth
	}
	return SideUnknown
}

// Env values used by the mrpack format.
const (
	EnvRequired    = "required"
	EnvOptional    = "optional"
	EnvUnsupported = "unsupported"
)

// SideFromEnv derives a Side from client/server support levels
// (Modrinth's legacy client_side/server_side or the mrpack env object).
func SideFromEnv(client, server string) Side {
	c, s := normEnv(client), normEnv(server)
	switch {
	case c == "" && s == "":
		return SideUnknown
	case c == EnvUnsupported && s == EnvUnsupported:
		return SideUnknown
	case s == EnvUnsupported:
		return SideClient
	case c == EnvUnsupported:
		return SideServer
	case c == "unknown" && s == "unknown":
		return SideUnknown
	// A mod required on one side and merely optional on the other is a
	// one-sided mod for packaging purposes.
	case c == EnvRequired && s == EnvOptional:
		return SideClient
	case s == EnvRequired && c == EnvOptional:
		return SideServer
	}
	return SideBoth
}

func normEnv(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// SideFromModrinthEnvironment maps Modrinth's newer single "environment"
// field (2025+) to a Side. ok is false for unknown/empty values.
func SideFromModrinthEnvironment(env string) (Side, bool) {
	switch env {
	case "client_only", "client_only_server_optional", "singleplayer_only":
		return SideClient, true
	case "server_only", "server_only_client_optional", "dedicated_server_only":
		return SideServer, true
	case "client_and_server", "client_or_server", "client_or_server_prefers_both":
		return SideBoth, true
	}
	return SideUnknown, false
}

// EnvForSide returns the mrpack env pair for a side.
func EnvForSide(s Side) (client, server string) {
	switch s {
	case SideClient:
		return EnvRequired, EnvUnsupported
	case SideServer:
		return EnvUnsupported, EnvRequired
	}
	return EnvRequired, EnvRequired
}

// Source identifies a mod hosting platform.
type Source string

const (
	SourceModrinth   Source = "modrinth"
	SourceCurseForge Source = "curseforge"
	SourceLocal      Source = "local"
)

func (s Source) Label() string {
	switch s {
	case SourceModrinth:
		return "Modrinth"
	case SourceCurseForge:
		return "CurseForge"
	}
	return "Other"
}

// ModrinthRef points at a Modrinth project version.
type ModrinthRef struct {
	ProjectID     string `json:"projectId"`
	VersionID     string `json:"versionId,omitempty"`
	VersionNumber string `json:"versionNumber,omitempty"`
}

// CurseForgeRef points at a CurseForge file.
type CurseForgeRef struct {
	ProjectID int  `json:"projectId"`
	FileID    int  `json:"fileId"`
	Required  bool `json:"required"`
}

// LocalRef is what a jar's own metadata (fabric.mod.json, mods.toml, …) says
// about it. It is recorded for jars added from disk.
type LocalRef struct {
	ModID   string   `json:"modId,omitempty"`   // the loader's mod id, e.g. "sodium"
	Version string   `json:"version,omitempty"` // the jar's version string
	Loaders []string `json:"loaders,omitempty"` // fabric, quilt, forge, neoforge
}

// File describes the jar (or zip) a mod entry installs.
type File struct {
	Path     string   `json:"path"` // relative to the instance root, e.g. mods/sodium.jar
	Size     int64    `json:"size,omitempty"`
	SHA1     string   `json:"sha1,omitempty"`
	SHA512   string   `json:"sha512,omitempty"`
	MD5      string   `json:"md5,omitempty"`
	URLs     []string `json:"urls,omitempty"`
	Filename string   `json:"-"`
	// Data is the file itself when the pack stores it: a jar added from disk,
	// or one found in an imported pack's overrides. Such files have no
	// download URL; exports bundle them. The .mpm format keeps them under
	// files/ rather than in the JSON.
	Data []byte `json:"-"`
}

// Name returns the file's base name.
func (f File) Name() string { return path.Base(f.Path) }

// Stored reports whether the pack carries the file itself.
func (f File) Stored() bool { return len(f.Data) > 0 }

// Mod is one entry in the pack.
type Mod struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Author      string         `json:"author,omitempty"`
	IconURL     string         `json:"iconUrl,omitempty"`
	ProjectType string         `json:"projectType,omitempty"` // mod, resourcepack, shader, datapack
	Side        Side           `json:"side"`
	SideLocked  bool           `json:"sideLocked,omitempty"` // user picked the side; never overwrite from metadata
	ClientEnv   string         `json:"clientEnv,omitempty"`
	ServerEnv   string         `json:"serverEnv,omitempty"`
	GroupID     string         `json:"group,omitempty"`
	Notes       string         `json:"notes,omitempty"`
	File        File           `json:"file"`
	Modrinth    *ModrinthRef   `json:"modrinth,omitempty"`
	CurseForge  *CurseForgeRef `json:"curseforge,omitempty"`
	Local       *LocalRef      `json:"local,omitempty"`
}

// PrimarySource is the platform the mod is best identified on.
func (m *Mod) PrimarySource() Source {
	switch {
	case m.Modrinth != nil:
		return SourceModrinth
	case m.CurseForge != nil:
		return SourceCurseForge
	}
	return SourceLocal
}

// SourceLabel says where the mod comes from, for display: the platform, or
// "Local file" for a jar the pack stores itself.
func (m *Mod) SourceLabel() string {
	if s := m.PrimarySource(); s != SourceLocal {
		return s.Label()
	}
	if m.File.Stored() || len(m.File.URLs) == 0 {
		return "Local file"
	}
	return SourceLocal.Label()
}

// DisplayName never returns an empty string.
func (m *Mod) DisplayName() string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	if m.File.Path != "" {
		n := m.File.Name()
		for _, ext := range []string{".jar", ".zip"} {
			if len(n) > len(ext) && strings.EqualFold(n[len(n)-len(ext):], ext) {
				return n[:len(n)-len(ext)]
			}
		}
		return n
	}
	if m.Slug != "" {
		return m.Slug
	}
	return "Unnamed mod"
}

// WebURL returns a project page URL if one is known.
func (m *Mod) WebURL() string {
	if m.Modrinth != nil {
		id := m.Slug
		if id == "" {
			id = m.Modrinth.ProjectID
		}
		return "https://modrinth.com/project/" + id
	}
	if m.CurseForge != nil && m.Slug != "" {
		return "https://www.curseforge.com/minecraft/mc-mods/" + m.Slug
	}
	return ""
}

// EffectiveSide treats Unknown as Both, which is the safe choice for exports.
func (m *Mod) EffectiveSide() Side {
	if m.Side == "" || m.Side == SideUnknown {
		return SideBoth
	}
	return m.Side
}

// Group is a user-defined category of mods.
type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// OverrideFile is a file from the pack's overrides folders.
type OverrideFile struct {
	// Side scopes the file: SideBoth = overrides/, SideClient = client-overrides/,
	// SideServer = server-overrides/.
	Side Side   `json:"side"`
	Path string `json:"path"` // relative to the instance root, e.g. config/foo.toml
	Data []byte `json:"-"`
}

// Loaders supported by the app.
var Loaders = []string{"fabric", "forge", "neoforge", "quilt"}

// LoaderLabel prettifies a loader id.
func LoaderLabel(l string) string {
	switch l {
	case "fabric":
		return "Fabric"
	case "forge":
		return "Forge"
	case "neoforge":
		return "NeoForge"
	case "quilt":
		return "Quilt"
	case "":
		return "None"
	}
	return l
}

// Pack is the whole modpack.
type Pack struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	Author        string          `json:"author,omitempty"`
	Summary       string          `json:"summary,omitempty"`
	Minecraft     string          `json:"minecraft"`
	Loader        string          `json:"loader"` // fabric, forge, neoforge, quilt
	LoaderVersion string          `json:"loaderVersion,omitempty"`
	Groups        []*Group        `json:"groups"`
	Mods          []*Mod          `json:"mods"`
	Overrides     []*OverrideFile `json:"-"`
}

// New returns an empty pack with sensible defaults.
func New(name string) *Pack {
	if name == "" {
		name = "New Modpack"
	}
	return &Pack{Name: name, Version: "1.0.0", Minecraft: "1.21.1", Loader: "fabric"}
}

// NewID returns a short random identifier.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddMod appends a mod, assigning an ID if needed. It returns false (and does
// nothing) when an equivalent mod is already present.
func (p *Pack) AddMod(m *Mod) bool {
	if p.FindDuplicate(m) != nil {
		return false
	}
	if m.ID == "" {
		m.ID = NewID()
	}
	if m.Side == "" {
		m.Side = SideUnknown
	}
	p.Mods = append(p.Mods, m)
	return true
}

// FindDuplicate returns an existing mod that refers to the same project or file.
func (p *Pack) FindDuplicate(m *Mod) *Mod {
	for _, o := range p.Mods {
		if o == m {
			continue
		}
		if m.Modrinth != nil && o.Modrinth != nil && m.Modrinth.ProjectID != "" && m.Modrinth.ProjectID == o.Modrinth.ProjectID {
			return o
		}
		if m.CurseForge != nil && o.CurseForge != nil && m.CurseForge.ProjectID != 0 && m.CurseForge.ProjectID == o.CurseForge.ProjectID {
			return o
		}
		if m.File.SHA1 != "" && strings.EqualFold(m.File.SHA1, o.File.SHA1) {
			return o
		}
	}
	return nil
}

// ModByID finds a mod.
func (p *Pack) ModByID(id string) *Mod {
	for _, m := range p.Mods {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// RemoveMod deletes a mod by ID.
func (p *Pack) RemoveMod(id string) bool {
	for i, m := range p.Mods {
		if m.ID == id {
			p.Mods = append(p.Mods[:i], p.Mods[i+1:]...)
			return true
		}
	}
	return false
}

// Group returns the group with the given ID or nil.
func (p *Pack) Group(id string) *Group {
	for _, g := range p.Groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// GroupByName finds a group case-insensitively.
func (p *Pack) GroupByName(name string) *Group {
	for _, g := range p.Groups {
		if strings.EqualFold(g.Name, strings.TrimSpace(name)) {
			return g
		}
	}
	return nil
}

// AddGroup creates a group (or returns the existing one with that name).
func (p *Pack) AddGroup(name string) *Group {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "New group"
	}
	if g := p.GroupByName(name); g != nil {
		return g
	}
	g := &Group{ID: NewID(), Name: name}
	p.Groups = append(p.Groups, g)
	return g
}

// RemoveGroup deletes a group; its mods become ungrouped.
func (p *Pack) RemoveGroup(id string) {
	for i, g := range p.Groups {
		if g.ID == id {
			p.Groups = append(p.Groups[:i], p.Groups[i+1:]...)
			break
		}
	}
	for _, m := range p.Mods {
		if m.GroupID == id {
			m.GroupID = ""
		}
	}
}

// MoveGroup moves a group up (-1) or down (+1).
func (p *Pack) MoveGroup(id string, delta int) {
	for i, g := range p.Groups {
		if g.ID == id {
			j := i + delta
			if j < 0 || j >= len(p.Groups) {
				return
			}
			p.Groups[i], p.Groups[j] = p.Groups[j], p.Groups[i]
			return
		}
	}
}

// GroupName returns the group's name or "Ungrouped".
func (p *Pack) GroupName(id string) string {
	if g := p.Group(id); g != nil {
		return g.Name
	}
	return "Ungrouped"
}

// Section is a group with its (sorted) mods, used for display.
type Section struct {
	Group *Group // nil = ungrouped
	Mods  []*Mod
}

// Sections returns groups in order followed by ungrouped mods. Mods inside a
// section are sorted by name. Empty groups are included so users can see them.
func (p *Pack) Sections(filter func(*Mod) bool) []Section {
	byGroup := map[string][]*Mod{}
	for _, m := range p.Mods {
		if filter != nil && !filter(m) {
			continue
		}
		gid := m.GroupID
		if p.Group(gid) == nil {
			gid = ""
		}
		byGroup[gid] = append(byGroup[gid], m)
	}
	var out []Section
	for _, g := range p.Groups {
		out = append(out, Section{Group: g, Mods: sortMods(byGroup[g.ID])})
	}
	if len(byGroup[""]) > 0 || len(p.Groups) == 0 {
		out = append(out, Section{Group: nil, Mods: sortMods(byGroup[""])})
	}
	return out
}

func sortMods(ms []*Mod) []*Mod {
	sort.SliceStable(ms, func(i, j int) bool {
		return strings.ToLower(ms[i].DisplayName()) < strings.ToLower(ms[j].DisplayName())
	})
	return ms
}

// SideCounts tallies mods per side (unknown counted separately).
func (p *Pack) SideCounts() map[Side]int {
	c := map[Side]int{}
	for _, m := range p.Mods {
		s := m.Side
		if s == "" {
			s = SideUnknown
		}
		c[s]++
	}
	return c
}

// ProjectTypeFromPath guesses the content type from the install path.
func ProjectTypeFromPath(p string) string {
	switch {
	case strings.HasPrefix(p, "resourcepacks/"):
		return "resourcepack"
	case strings.HasPrefix(p, "shaderpacks/"):
		return "shader"
	case strings.HasPrefix(p, "datapacks/"), strings.Contains(p, "/datapacks/"):
		return "datapack"
	}
	return "mod"
}

// FolderForProjectType is the inverse of ProjectTypeFromPath.
func FolderForProjectType(t string) string {
	switch t {
	case "resourcepack":
		return "resourcepacks"
	case "shader", "shaderpack":
		return "shaderpacks"
	case "datapack":
		return "datapacks"
	}
	return "mods"
}
