package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"modpackmanager/internal/core"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

// instructions are given to the model by clients that support them.
const instructions = `Modpack Manager edits Minecraft modpacks stored as .mpm files on this computer.
Start from an existing .mpm, make a new one with create_pack, or turn a .mrpack or CurseForge .zip into one with import_pack.
Every tool that changes a pack saves the .mpm file straight away; there is no separate save step. If the pack is open in the Modpack Manager app, the app reloads it (or asks first when it has unsaved changes).
Refer to mods by the "id" that list_mods returns.
A mod's side decides which export it goes into: "client" mods only go to players, "server" mods only to the server, "both" to everyone. Unknown sides are treated as both.
To add a mod, find it with search_mods, then call add_mod with its id or slug; required dependencies are added automatically.
Use absolute paths (or ~/...) for files.`

// Timeouts for network work done on behalf of a tool.
const (
	enrichTimeout = 3 * time.Minute
	lookupTimeout = 30 * time.Second
	addTimeout    = 3 * time.Minute
	exportTimeout = 15 * time.Minute
)

type tool struct {
	name, title, description string
	schema                   map[string]any
	readOnly                 bool // doesn't change files
	destructive              bool // may remove things (for tools that change files)
	idempotent               bool // repeating the call changes nothing more
	openWorld                bool // talks to Modrinth/CurseForge
	run                      func(ctx context.Context, s *Server, args json.RawMessage) (any, error)
}

func (t *tool) definition(e era) map[string]any {
	d := map[string]any{"name": t.name, "description": t.description, "inputSchema": t.schema}
	if e.at("2025-06-18") {
		d["title"] = t.title
	}
	if e.at("2025-03-26") {
		ann := map[string]any{"title": t.title, "readOnlyHint": t.readOnly, "openWorldHint": t.openWorld}
		if !t.readOnly {
			ann["destructiveHint"] = t.destructive
			ann["idempotentHint"] = t.idempotent
		}
		d["annotations"] = ann
	}
	return d
}

// ---------------------------------------------------------------------------
// Schemas

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func choice(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

func integer(desc string, lo, hi int) map[string]any {
	return map[string]any{"type": "integer", "minimum": lo, "maximum": hi, "description": desc}
}

func stringList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": desc}
}

const (
	descPack     = "Path of the .mpm pack file (absolute, or starting with ~/)."
	descAnyPack  = "Path of a pack file: .mpm, .mrpack or CurseForge .zip (absolute, or starting with ~/)."
	descMods     = "The mods: ids from list_mods (most reliable), or names, file names, Modrinth slugs or CurseForge project ids."
	descSide     = "client: only players need it. server: only the server. both: everyone."
	descGroup    = "Group to put the mods in, by name; it is created if it doesn't exist."
	descOverride = "Replace the file if it already exists."
)

func allTools() []*tool {
	return []*tool{
		{
			name: "pack_info", title: "Pack overview", readOnly: true, openWorld: true,
			description: "Summarise a modpack: name, Minecraft version, loader, how many mods are client-only, server-only or needed on both sides, its groups, override files and stored jars. Works on .mpm, .mrpack and CurseForge .zip files (for the last two, mod details are looked up online).",
			schema:      object([]string{"pack"}, map[string]any{"pack": str(descAnyPack)}),
			run:         packInfo,
		},
		{
			name: "list_mods", title: "List mods", readOnly: true, openWorld: true,
			description: "List the mods in a pack, grouped like the app shows them, with each mod's id (for the other tools), side, source, group, file and version. Works on .mpm, .mrpack and CurseForge .zip files (for the last two, mod details are looked up online).",
			schema: object([]string{"pack"}, map[string]any{
				"pack":   str(descAnyPack),
				"filter": str("Optional filter, like the app's search box: words match names and summaries; also side:client|server|both|unknown, source:modrinth|curseforge|local, group:<name>, type:mod|resourcepack|shader|datapack. Quote words with spaces."),
				"limit":  integer("Most mods to return (default 500).", 1, 5000),
				"offset": integer("Skip this many mods first, for paging.", 0, 100000),
			}),
			run: listMods,
		},
		{
			name: "search_mods", title: "Search Modrinth and CurseForge", readOnly: true, openWorld: true,
			description: "Search Modrinth (and CurseForge, when an API key is set) for mods, resource packs or shaders. Pass pack to only get results for that pack's Minecraft version and loader, and to see which results it already has.",
			schema: object([]string{"query"}, map[string]any{
				"query":     str("What to look for."),
				"pack":      str("Optional .mpm (or .mrpack/.zip) pack: filters by its Minecraft version and loader and marks results it already contains."),
				"source":    choice("Where to search (default modrinth; curseforge needs an API key in the app's preferences).", "modrinth", "curseforge", "both"),
				"type":      choice("What kind of project (default mod).", "mod", "resourcepack", "shader"),
				"minecraft": str("Minecraft version to filter by, e.g. 1.21.1 (default: the pack's)."),
				"loader":    choice("Mod loader to filter by (default: the pack's).", "fabric", "quilt", "forge", "neoforge"),
				"limit":     integer("Most results (default 10).", 1, 50),
			}),
			run: searchMods,
		},
		{
			name: "create_pack", title: "Create a pack", openWorld: true,
			description: "Create a new, empty .mpm pack. Without loaderVersion, the newest stable loader version for that Minecraft version is looked up online.",
			schema: object([]string{"pack", "name", "minecraft", "loader"}, map[string]any{
				"pack":          str("Where to save the new .mpm file (absolute, or starting with ~/)."),
				"name":          str("Pack name."),
				"minecraft":     str("Minecraft version, e.g. 1.21.1."),
				"loader":        choice("Mod loader.", "fabric", "quilt", "forge", "neoforge"),
				"loaderVersion": str("Loader version (default: the newest stable one)."),
				"version":       str("Pack version (default 1.0.0)."),
				"author":        str("Pack author."),
				"overwrite":     flag(descOverride),
			}),
			run: createPack,
		},
		{
			name: "import_pack", title: "Import a pack", openWorld: true,
			description: "Turn a Modrinth .mrpack or a CurseForge .zip into an .mpm pack that the other tools can edit. Mod names, sides and icons are looked up online.",
			schema: object([]string{"file", "pack"}, map[string]any{
				"file":      str("The .mrpack or CurseForge .zip to import (absolute, or starting with ~/)."),
				"pack":      str("Where to save the .mpm file."),
				"overwrite": flag(descOverride),
			}),
			run: importPack,
		},
		{
			name: "update_pack_info", title: "Change pack details", idempotent: true,
			description: "Change a pack's name, version, author, summary or loader version. Fields that are left out stay as they are.",
			schema: object([]string{"pack"}, map[string]any{
				"pack":          str(descPack),
				"name":          str("New pack name."),
				"version":       str("New pack version, e.g. 1.1.0."),
				"author":        str("New author."),
				"summary":       str("New one-line summary."),
				"loaderVersion": str("New loader version (same loader)."),
			}),
			run: updatePackInfo,
		},
		{
			name: "add_mod", title: "Add a mod", openWorld: true,
			description: "Add a Modrinth or CurseForge project to a pack, picking the newest version that fits the pack's Minecraft version and loader. Its required dependencies are added too. Use search_mods to find the project.",
			schema: object([]string{"pack", "project"}, map[string]any{
				"pack":    str(descPack),
				"project": str("Modrinth project slug or id (e.g. sodium), or a CurseForge project id (a number)."),
				"source":  choice("Where the project is (default modrinth).", "modrinth", "curseforge"),
				"group":   str(descGroup),
				"side":    choice("Set and lock the mod's side instead of using its metadata. "+descSide, "client", "server", "both"),
			}),
			run: addMod,
		},
		{
			name: "add_jars", title: "Add jar files", openWorld: true,
			description: "Add .jar files from this computer to a pack (a folder adds the jars directly inside it). Jars that are on Modrinth get linked; others are stored inside the .mpm and bundled into exports. Name, version and side come from the jar's own metadata. A newer build of a stored jar replaces the older one.",
			schema: object([]string{"pack", "files"}, map[string]any{
				"pack":    str(descPack),
				"files":   stringList("Paths of .jar files or folders (absolute, or starting with ~/)."),
				"group":   str(descGroup),
				"side":    choice("Set and lock the side of every added jar. "+descSide, "client", "server", "both"),
				"offline": flag("Don't look the jars up on Modrinth; store them all."),
			}),
			run: addJars,
		},
		{
			name: "remove_mods", title: "Remove mods", destructive: true, idempotent: true,
			description: "Remove mods from a pack. Mods that depend on them are not removed.",
			schema:      object([]string{"pack", "mods"}, map[string]any{"pack": str(descPack), "mods": stringList(descMods)}),
			run:         removeMods,
		},
		{
			name: "set_side", title: "Set mod sides", idempotent: true,
			description: "Set which side mods are needed on, which decides the export they go into. The side is locked by default, so refreshed metadata won't change it.",
			schema: object([]string{"pack", "mods", "side"}, map[string]any{
				"pack": str(descPack),
				"mods": stringList(descMods),
				"side": choice(descSide, "client", "server", "both"),
				"lock": flag("Keep this side when mod info is refreshed (default true)."),
			}),
			run: setSide,
		},
		{
			name: "move_to_group", title: "Group mods", idempotent: true,
			description: "Put mods in a group (created if needed), or take them out of their group with an empty group name.",
			schema: object([]string{"pack", "mods", "group"}, map[string]any{
				"pack":  str(descPack),
				"mods":  stringList(descMods),
				"group": str("Group name; empty to ungroup."),
			}),
			run: moveToGroup,
		},
		{
			name: "export_pack", title: "Export a pack", openWorld: true,
			description: "Export the client, common, server or full part of a pack as a Modrinth .mrpack, a CurseForge .zip, or a plain .zip with the jars in mods/ (for servers). Jars may be downloaded. Server exports include the common mods by default; client exports don't unless includeCommon is set.",
			schema: object([]string{"pack", "target"}, map[string]any{
				"pack":             str(descAnyPack),
				"target":           choice("client: client-only mods; common: mods for both sides; server: server-only mods; full: everything.", "client", "common", "server", "full"),
				"format":           choice("File format (default mrpack, or zip for server).", "mrpack", "curseforge", "zip"),
				"output":           str("Where to write the file (default: next to the pack, named <pack>-<version>-<target>)."),
				"includeCommon":    flag("Add the mods both sides need to a client or server export, making it complete."),
				"embedNonModrinth": flag("In an .mrpack, bundle jars that aren't on Modrinth instead of linking to them."),
				"overwrite":        flag(descOverride),
			}),
			run: exportPack,
		},
	}
}

// ---------------------------------------------------------------------------
// Results

type packSummary struct {
	Path          string         `json:"path"`
	Format        string         `json:"format"`
	Name          string         `json:"name"`
	Version       string         `json:"version,omitempty"`
	Author        string         `json:"author,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	Minecraft     string         `json:"minecraft"`
	Loader        string         `json:"loader"`
	LoaderVersion string         `json:"loaderVersion,omitempty"`
	Mods          int            `json:"mods"`
	Sides         map[string]int `json:"sides"`
	Groups        []groupInfo    `json:"groups,omitempty"`
	OverrideFiles int            `json:"overrideFiles"`
	StoredJars    int            `json:"storedJars,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

type groupInfo struct {
	Name string `json:"name"`
	Mods int    `json:"mods"`
}

func summarize(path string, doc *core.Document, warnings []string) *packSummary {
	p := doc.Pack
	out := &packSummary{
		Path: path, Format: string(doc.Kind), Name: p.Name, Version: p.Version, Author: p.Author, Summary: p.Summary,
		Minecraft: p.Minecraft, Loader: p.Loader, LoaderVersion: p.LoaderVersion, Mods: len(p.Mods),
		Sides: map[string]int{}, OverrideFiles: len(p.Overrides), StoredJars: core.CountStored(p.Mods), Warnings: warnings,
	}
	for _, m := range p.Mods {
		out.Sides[string(sideOf(m))]++
	}
	for _, sec := range p.Sections(nil) {
		if sec.Group != nil {
			out.Groups = append(out.Groups, groupInfo{Name: sec.Group.Name, Mods: len(sec.Mods)})
		}
	}
	return out
}

func sideOf(m *model.Mod) model.Side {
	if m.Side == "" {
		return model.SideUnknown
	}
	return m.Side
}

type modInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Side         string `json:"side"`
	SideLocked   bool   `json:"sideLocked,omitempty"`
	Source       string `json:"source"`
	Type         string `json:"type,omitempty"`
	Group        string `json:"group,omitempty"`
	File         string `json:"file"`
	Version      string `json:"version,omitempty"`
	Slug         string `json:"slug,omitempty"`
	ModrinthID   string `json:"modrinthId,omitempty"`
	CurseForgeID int    `json:"curseforgeId,omitempty"`
	Stored       bool   `json:"stored,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

func describeMod(p *model.Pack, m *model.Mod) modInfo {
	mi := modInfo{
		ID: m.ID, Name: m.DisplayName(), Side: string(sideOf(m)), SideLocked: m.SideLocked,
		Source: string(m.PrimarySource()), File: m.File.Path, Slug: m.Slug, Stored: m.File.Stored(),
		Summary: truncate(m.Summary, 200), Notes: m.Notes,
	}
	if m.ProjectType != "" && m.ProjectType != "mod" {
		mi.Type = m.ProjectType
	}
	if m.GroupID != "" {
		mi.Group = p.GroupName(m.GroupID)
	}
	switch {
	case m.Modrinth != nil:
		mi.ModrinthID, mi.Version = m.Modrinth.ProjectID, m.Modrinth.VersionNumber
	case m.Local != nil:
		mi.Version = m.Local.Version
	}
	if m.CurseForge != nil {
		mi.CurseForgeID = m.CurseForge.ProjectID
	}
	return mi
}

func describeMods(p *model.Pack, ms []*model.Mod) []modInfo {
	out := make([]modInfo, 0, len(ms))
	for _, m := range ms {
		out = append(out, describeMod(p, m))
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n])) + "…"
}

// ---------------------------------------------------------------------------
// Arguments and files

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return v, nil
}

// resolvePath expands ~ and makes the path absolute.
func resolvePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("a file path is required")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

func hasExt(p, ext string) bool { return strings.EqualFold(filepath.Ext(p), ext) }

// sameFile reports whether a and b name the same file, also on
// case-insensitive file systems.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// withWarnings adds warnings to a result when there are any.
func withWarnings(m map[string]any, warnings []string) map[string]any {
	if len(warnings) > 0 {
		m["warnings"] = warnings
	}
	return m
}

// openAny opens any pack file for reading, looking up mod details when the
// format doesn't carry them.
func (s *Server) openAny(ctx context.Context, arg string) (string, *core.Document, []string, error) {
	path, err := resolvePath(arg)
	if err != nil {
		return "", nil, nil, err
	}
	doc, err := core.Open(path)
	if err != nil {
		return "", nil, nil, err
	}
	var warnings []string
	if doc.NeedsEnrichment() {
		ectx, cancel := context.WithTimeout(ctx, enrichTimeout)
		rep := s.svc.Enrich(ectx, doc.Pack, nil)
		cancel()
		warnings = rep.Warnings
	}
	return path, doc, warnings, nil
}

// openMPM opens an .mpm pack for changing. Call with writeMu held.
func openMPM(arg string) (string, *core.Document, error) {
	path, err := resolvePath(arg)
	if err != nil {
		return "", nil, err
	}
	if !hasExt(path, formats.MPMExtension) {
		return "", nil, fmt.Errorf("%s isn't an .mpm pack; turn it into one with import_pack first", filepath.Base(path))
	}
	doc, err := core.Open(path)
	if err != nil {
		return "", nil, err
	}
	return path, doc, nil
}

// outputPath resolves where a new file goes and refuses to replace one
// unless asked to.
func outputPath(arg, ext string, overwrite bool) (string, error) {
	path, err := resolvePath(arg)
	if err != nil {
		return "", err
	}
	if !hasExt(path, ext) {
		for _, other := range []string{".mpm", ".mrpack", ".zip"} {
			if hasExt(path, other) {
				return "", fmt.Errorf("%s must end in %s", filepath.Base(path), ext)
			}
		}
		path += ext
	}
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("%s is a folder", path)
		}
		if !overwrite {
			return "", fmt.Errorf("%s already exists; pass overwrite: true to replace it", path)
		}
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		return "", fmt.Errorf("the folder %s doesn't exist", filepath.Dir(path))
	}
	return path, nil
}

// findMods resolves references to mods in a pack.
func findMods(p *model.Pack, refs []string) ([]*model.Mod, error) {
	if len(refs) == 0 {
		return nil, errors.New("name at least one mod")
	}
	var out []*model.Mod
	seen := map[string]bool{}
	var problems []string
	for _, ref := range refs {
		ms := matchMod(p, strings.TrimSpace(ref))
		switch len(ms) {
		case 0:
			problems = append(problems, fmt.Sprintf("no mod matches %q", ref))
		case 1:
			if !seen[ms[0].ID] {
				seen[ms[0].ID] = true
				out = append(out, ms[0])
			}
		default:
			var names []string
			for _, m := range ms {
				names = append(names, fmt.Sprintf("%s (%s)", m.DisplayName(), m.ID))
			}
			problems = append(problems, fmt.Sprintf("%q matches %d mods: %s; use an id from list_mods", ref, len(ms), strings.Join(names, ", ")))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

func matchMod(p *model.Pack, ref string) []*model.Mod {
	if ref == "" {
		return nil
	}
	if m := p.ModByID(ref); m != nil {
		return []*model.Mod{m}
	}
	var out []*model.Mod
	for _, m := range p.Mods {
		if strings.EqualFold(m.DisplayName(), ref) || strings.EqualFold(m.File.Name(), ref) || strings.EqualFold(m.File.Path, ref) ||
			(m.Slug != "" && strings.EqualFold(m.Slug, ref)) ||
			(m.Modrinth != nil && strings.EqualFold(m.Modrinth.ProjectID, ref)) ||
			(m.CurseForge != nil && strconv.Itoa(m.CurseForge.ProjectID) == ref) ||
			(m.Local != nil && m.Local.ModID != "" && strings.EqualFold(m.Local.ModID, ref)) {
			out = append(out, m)
		}
	}
	return out
}

func parseSide(v string) (model.Side, error) {
	switch v {
	case "":
		return "", nil
	case "client", "server", "both":
		return model.Side(v), nil
	}
	return "", fmt.Errorf("side must be client, server or both, not %q", v)
}

// ---------------------------------------------------------------------------
// Read-only tools

func packInfo(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct{ Pack string }](raw)
	if err != nil {
		return nil, err
	}
	path, doc, warnings, err := s.openAny(ctx, args.Pack)
	if err != nil {
		return nil, err
	}
	return summarize(path, doc, warnings), nil
}

func listMods(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack, Filter  string
		Limit, Offset int
	}](raw)
	if err != nil {
		return nil, err
	}
	_, doc, warnings, err := s.openAny(ctx, args.Pack)
	if err != nil {
		return nil, err
	}
	p := doc.Pack
	f := core.ParseFilter(args.Filter)
	var ms []*model.Mod
	for _, sec := range p.Sections(func(m *model.Mod) bool { return f.Match(p, m) }) {
		ms = append(ms, sec.Mods...)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 500
	}
	total := len(ms)
	ms = ms[min(max(args.Offset, 0), total):]
	if len(ms) > limit {
		ms = ms[:limit]
	}
	return withWarnings(map[string]any{"total": total, "offset": args.Offset, "mods": describeMods(p, ms)}, warnings), nil
}

type hitInfo struct {
	Source    string   `json:"source"`
	ID        string   `json:"id"`
	Slug      string   `json:"slug,omitempty"`
	Title     string   `json:"title"`
	Author    string   `json:"author,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Downloads int64    `json:"downloads"`
	Side      string   `json:"side"`
	Type      string   `json:"type,omitempty"`
	URL       string   `json:"url"`
	AlsoOn    string   `json:"alsoOn,omitempty"`
	InPack    bool     `json:"inPack,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

func searchMods(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Query, Pack, Source, Type, Minecraft, Loader string
		Limit                                        int
	}](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Query) == "" {
		return nil, errors.New("query is required")
	}
	req := core.SearchRequest{Query: args.Query, ProjectType: args.Type, Minecraft: args.Minecraft, Loader: args.Loader, PageSize: args.Limit}
	if req.PageSize <= 0 {
		req.PageSize = 10
	}
	var pack *model.Pack
	var warnings []string
	if args.Pack != "" {
		_, doc, w, err := s.openAny(ctx, args.Pack)
		if err != nil {
			return nil, err
		}
		pack, warnings = doc.Pack, w
		if req.Minecraft == "" {
			req.Minecraft = pack.Minecraft
		}
		if req.Loader == "" && (req.ProjectType == "" || req.ProjectType == "mod") {
			req.Loader = pack.Loader
		}
	}
	switch args.Source {
	case "", "modrinth":
		req.Sources = []model.Source{model.SourceModrinth}
	case "curseforge":
		req.Sources = []model.Source{model.SourceCurseForge}
	case "both":
		req.Sources = []model.Source{model.SourceModrinth, model.SourceCurseForge}
	default:
		return nil, fmt.Errorf("source must be modrinth, curseforge or both, not %q", args.Source)
	}
	if slices.Contains(req.Sources, model.SourceCurseForge) && !s.svc.HasCurseForge() {
		if len(req.Sources) == 1 {
			return nil, errors.New("searching CurseForge needs an API key: add one in the app's preferences, or set MPM_CURSEFORGE_API_KEY")
		}
		req.Sources = []model.Source{model.SourceModrinth}
		warnings = append(warnings, "CurseForge was skipped: it needs an API key (app preferences or MPM_CURSEFORGE_API_KEY)")
	}
	sctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	res := s.svc.Search(sctx, req)
	for _, src := range req.Sources {
		if err := res.Errors[src]; err != nil {
			warnings = append(warnings, fmt.Sprintf("%s search failed: %v", src.Label(), err))
		}
	}
	if len(res.Hits) == 0 && len(res.Errors) == len(req.Sources) {
		return nil, errors.New(strings.Join(warnings, "; "))
	}
	hits := make([]hitInfo, 0, len(res.Hits))
	for _, h := range res.Hits {
		hi := hitInfo{
			Source: string(h.Source), ID: h.ID, Slug: h.Slug, Title: h.Title, Author: h.Author,
			Summary: truncate(h.Summary, 240), Downloads: h.Downloads, Side: string(h.Side), URL: h.WebURL(),
			AlsoOn: string(h.AlsoOn), Tags: h.Categories,
		}
		if h.ProjectType != "" && h.ProjectType != "mod" {
			hi.Type = h.ProjectType
		}
		if hi.Side == "" {
			hi.Side = string(model.SideUnknown)
		}
		if pack != nil {
			hi.InPack = inPack(pack, h)
		}
		hits = append(hits, hi)
	}
	return withWarnings(map[string]any{"total": res.Total, "results": hits, "minecraft": req.Minecraft, "loader": req.Loader}, warnings), nil
}

func inPack(p *model.Pack, h *core.Hit) bool {
	for _, m := range p.Mods {
		switch h.Source {
		case model.SourceModrinth:
			if m.Modrinth != nil && m.Modrinth.ProjectID == h.ID {
				return true
			}
		case model.SourceCurseForge:
			if m.CurseForge != nil && strconv.Itoa(m.CurseForge.ProjectID) == h.ID {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tools that change files

func createPack(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack, Name, Minecraft, Loader, LoaderVersion, Version, Author string
		Overwrite                                                     bool
	}](raw)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(args.Name) == "":
		return nil, errors.New("name is required")
	case strings.TrimSpace(args.Minecraft) == "":
		return nil, errors.New("minecraft (the Minecraft version) is required")
	case !slices.Contains(model.Loaders, args.Loader):
		return nil, fmt.Errorf("loader must be one of %s", strings.Join(model.Loaders, ", "))
	}
	lv := strings.TrimSpace(args.LoaderVersion)
	var warnings []string
	if lv == "" {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		vs, err := core.LoaderVersions(lctx, args.Loader, args.Minecraft)
		cancel()
		switch {
		case err != nil:
			return nil, fmt.Errorf("couldn't look up %s versions for Minecraft %s (%v); pass loaderVersion", model.LoaderLabel(args.Loader), args.Minecraft, err)
		case len(vs) == 0:
			return nil, fmt.Errorf("%s has no version for Minecraft %s", model.LoaderLabel(args.Loader), args.Minecraft)
		}
		lv = vs[0]
		warnings = append(warnings, fmt.Sprintf("Using %s %s, the newest stable version for Minecraft %s.", model.LoaderLabel(args.Loader), lv, args.Minecraft))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	path, err := outputPath(args.Pack, formats.MPMExtension, args.Overwrite)
	if err != nil {
		return nil, err
	}
	doc := core.NewDocument(strings.TrimSpace(args.Name), strings.TrimSpace(args.Minecraft), args.Loader, lv)
	if v := strings.TrimSpace(args.Version); v != "" {
		doc.Pack.Version = v
	}
	doc.Pack.Author = strings.TrimSpace(args.Author)
	if err := doc.Save(path); err != nil {
		return nil, err
	}
	return summarize(path, doc, warnings), nil
}

func importPack(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		File, Pack string
		Overwrite  bool
	}](raw)
	if err != nil {
		return nil, err
	}
	_, doc, warnings, err := s.openAny(ctx, args.File)
	if err != nil {
		return nil, err
	}
	if doc.Kind == formats.KindMPM {
		return nil, errors.New("that file is already an .mpm pack; the other tools can use it as it is")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	path, err := outputPath(args.Pack, formats.MPMExtension, args.Overwrite)
	if err != nil {
		return nil, err
	}
	if err := doc.Save(path); err != nil {
		return nil, err
	}
	return summarize(path, doc, warnings), nil
}

func updatePackInfo(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack                                          string
		Name, Version, Author, Summary, LoaderVersion *string
	}](raw)
	if err != nil {
		return nil, err
	}
	if args.Name == nil && args.Version == nil && args.Author == nil && args.Summary == nil && args.LoaderVersion == nil {
		return nil, errors.New("pass at least one of name, version, author, summary or loaderVersion")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	path, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	p := doc.Pack
	set := func(dst *string, v *string, required string) error {
		if v == nil {
			return nil
		}
		if t := strings.TrimSpace(*v); t != "" || required == "" {
			*dst = t
			return nil
		}
		return fmt.Errorf("%s can't be empty", required)
	}
	for _, f := range []struct {
		dst      *string
		v        *string
		required string
	}{{&p.Name, args.Name, "name"}, {&p.Version, args.Version, ""}, {&p.Author, args.Author, ""}, {&p.Summary, args.Summary, ""}, {&p.LoaderVersion, args.LoaderVersion, "loaderVersion"}} {
		if err := set(f.dst, f.v, f.required); err != nil {
			return nil, err
		}
	}
	if err := doc.Save(""); err != nil {
		return nil, err
	}
	return summarize(path, doc, nil), nil
}

func addMod(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct{ Pack, Project, Source, Group, Side string }](raw)
	if err != nil {
		return nil, err
	}
	project := strings.TrimSpace(args.Project)
	if project == "" {
		return nil, errors.New("project is required")
	}
	src := model.SourceModrinth
	switch args.Source {
	case "", "modrinth":
	case "curseforge":
		if _, err := strconv.Atoi(project); err != nil {
			return nil, errors.New("for CurseForge, pass the numeric project id (search_mods shows it)")
		}
		if !s.svc.HasCurseForge() {
			return nil, errors.New("adding CurseForge mods needs an API key: add one in the app's preferences, or set MPM_CURSEFORGE_API_KEY")
		}
		src = model.SourceCurseForge
	default:
		return nil, fmt.Errorf("source must be modrinth or curseforge, not %q", args.Source)
	}
	side, err := parseSide(args.Side)
	if err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	p := doc.Pack
	groupID := ""
	if g := strings.TrimSpace(args.Group); g != "" {
		groupID = p.AddGroup(g).ID
	}
	actx, cancel := context.WithTimeout(ctx, addTimeout)
	defer cancel()
	res, err := s.svc.AddProject(actx, p, src, project, groupID)
	if err != nil {
		return nil, err
	}
	// AddProject adds the requested project before its dependencies, and adds
	// nothing when that project is already in the pack.
	if side != "" && len(res.Added) > 0 {
		res.Added[0].Side, res.Added[0].SideLocked = side, true
	}
	if len(res.Added) > 0 {
		if err := doc.Save(""); err != nil {
			return nil, err
		}
	}
	out := map[string]any{"added": describeMods(p, res.Added)}
	if len(res.Existing) > 0 {
		out["alreadyInPack"] = describeMods(p, res.Existing)
	}
	return withWarnings(out, res.Warnings), nil
}

type jarOutcome struct {
	File     string   `json:"file"`
	Status   string   `json:"status"` // added, linked, updated, unchanged, failed
	Mod      *modInfo `json:"mod,omitempty"`
	Replaced string   `json:"replaced,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func addJars(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack, Group, Side string
		Files             []string
		Offline           bool
	}](raw)
	if err != nil {
		return nil, err
	}
	if len(args.Files) == 0 {
		return nil, errors.New("files is required")
	}
	side, err := parseSide(args.Side)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(args.Files))
	for _, f := range args.Files {
		p, err := resolvePath(f)
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	// The slow part (reading, the Modrinth lookup) happens before taking the lock.
	pctx, cancel := context.WithTimeout(ctx, addTimeout)
	prep := s.svc.PrepareJars(pctx, core.ReadJarFiles(paths), core.JarOptions{Side: side, Offline: args.Offline})
	cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	p := doc.Pack
	results := prep.Apply(p)
	if g := strings.TrimSpace(args.Group); g != "" && core.JarsChanged(results) {
		gid := p.AddGroup(g).ID
		for _, r := range results {
			if r.Err == nil && !r.Existing && r.Mod != nil {
				r.Mod.GroupID = gid
			}
		}
	}
	if core.JarsChanged(results) {
		if err := doc.Save(""); err != nil {
			return nil, err
		}
	}
	outcomes := make([]jarOutcome, 0, len(results))
	for _, r := range results {
		o := jarOutcome{File: r.Name, Warnings: r.Warnings}
		switch {
		case r.Err != nil:
			o.Status, o.Error = "failed", r.Name+" "+r.Err.Error()
		case r.Existing:
			o.Status = "unchanged"
		case r.Replaced != nil:
			o.Status, o.Replaced = "updated", r.Replaced.File.Name()
		case r.Linked:
			o.Status = "linked"
		default:
			o.Status = "added"
		}
		if r.Mod != nil {
			mi := describeMod(p, r.Mod)
			o.Mod = &mi
		}
		outcomes = append(outcomes, o)
	}
	title, _ := core.JarSummary(results, prep.Report)
	return withWarnings(map[string]any{"summary": title, "results": outcomes}, prep.Warnings), nil
}

func removeMods(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack string
		Mods []string
	}](raw)
	if err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	ms, err := findMods(doc.Pack, args.Mods)
	if err != nil {
		return nil, err
	}
	removed := describeMods(doc.Pack, ms)
	for _, m := range ms {
		doc.Pack.RemoveMod(m.ID)
	}
	if err := doc.Save(""); err != nil {
		return nil, err
	}
	return map[string]any{"removed": removed, "modsLeft": len(doc.Pack.Mods)}, nil
}

func setSide(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack, Side string
		Mods       []string
		Lock       *bool
	}](raw)
	if err != nil {
		return nil, err
	}
	side, err := parseSide(args.Side)
	if err != nil {
		return nil, err
	}
	if side == "" {
		return nil, errors.New("side is required")
	}
	lock := args.Lock == nil || *args.Lock
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	ms, err := findMods(doc.Pack, args.Mods)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		m.Side, m.SideLocked = side, lock
	}
	if err := doc.Save(""); err != nil {
		return nil, err
	}
	return map[string]any{"updated": describeMods(doc.Pack, ms)}, nil
}

func moveToGroup(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack  string
		Group *string
		Mods  []string
	}](raw)
	if err != nil {
		return nil, err
	}
	if args.Group == nil {
		return nil, errors.New(`group is required (use "" to take the mods out of their group)`)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, doc, err := openMPM(args.Pack)
	if err != nil {
		return nil, err
	}
	ms, err := findMods(doc.Pack, args.Mods)
	if err != nil {
		return nil, err
	}
	gid, name := "", strings.TrimSpace(*args.Group)
	if name != "" {
		g := doc.Pack.AddGroup(name)
		gid, name = g.ID, g.Name
	}
	for _, m := range ms {
		m.GroupID = gid
	}
	if err := doc.Save(""); err != nil {
		return nil, err
	}
	return map[string]any{"group": name, "moved": describeMods(doc.Pack, ms)}, nil
}

func exportPack(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	args, err := decode[struct {
		Pack, Target, Format, Output               string
		IncludeCommon, EmbedNonModrinth, Overwrite *bool
	}](raw)
	if err != nil {
		return nil, err
	}
	target := core.Target(args.Target)
	if !slices.Contains([]core.Target{core.TargetClient, core.TargetCommon, core.TargetServer, core.TargetFull}, target) {
		return nil, errors.New("target must be client, common, server or full")
	}
	opt := core.DefaultExportOptions(target)
	switch args.Format {
	case "":
	case "mrpack", "curseforge", "zip":
		opt.Format = core.Format(args.Format)
	default:
		return nil, errors.New("format must be mrpack, curseforge or zip")
	}
	if args.IncludeCommon != nil {
		opt.IncludeCommon = *args.IncludeCommon
	}
	if args.EmbedNonModrinth != nil {
		opt.EmbedNonModrinth = *args.EmbedNonModrinth
	}
	packPath, doc, warnings, err := s.openAny(ctx, args.Pack)
	if err != nil {
		return nil, err
	}
	out := args.Output
	if strings.TrimSpace(out) == "" {
		out = filepath.Join(filepath.Dir(packPath), core.SuggestedFileName(doc.Pack, opt))
	}
	overwrite := args.Overwrite != nil && *args.Overwrite
	path, err := outputPath(out, opt.Format.Ext(), overwrite)
	if err != nil {
		return nil, err
	}
	if sameFile(path, packPath) {
		return nil, errors.New("the export can't replace the pack itself; choose another output")
	}
	ectx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()
	data, res, err := s.svc.Export(ectx, doc.Pack, opt, nil, nil)
	if err != nil {
		return nil, err
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("%s appeared while exporting; pass overwrite: true to replace it", path)
		}
	}
	if err := formats.WriteFileAtomic(path, data); err != nil {
		return nil, err
	}
	result := map[string]any{
		"output": path, "target": string(opt.Target), "format": string(opt.Format), "includeCommon": opt.IncludeCommon,
		"mods": res.Mods, "bundledJars": res.Embedded, "bytes": len(data),
	}
	if len(res.Skipped) > 0 {
		result["skipped"] = res.Skipped
	}
	return withWarnings(result, append(warnings, res.Warnings...)), nil
}
