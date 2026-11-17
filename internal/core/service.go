// Package core ties the formats and API clients together into the operations
// the UIs offer: opening packs, enriching metadata, searching, adding mods
// (with dependencies) and exporting.
package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/api/modrinth"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

// Service is safe for concurrent use.
type Service struct {
	MR *modrinth.Client
	CF *curseforge.Client

	mu sync.Mutex
}

// New creates a service using the public API endpoints.
//
// MPM_MODRINTH_URL / MPM_CURSEFORGE_URL override the API endpoints (useful
// for self-hosted mirrors and for testing against a local mock).
func New(cfAPIKey string) *Service {
	s := &Service{MR: modrinth.New(), CF: curseforge.New(cfAPIKey)}
	if u := os.Getenv("MPM_MODRINTH_URL"); u != "" {
		s.MR.BaseURL = u
	}
	if u := os.Getenv("MPM_CURSEFORGE_URL"); u != "" {
		s.CF.BaseURL = u
	}
	return s
}

// SetCurseForgeKey updates the CurseForge API key.
func (s *Service) SetCurseForgeKey(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CF.APIKey = strings.TrimSpace(k)
}

// HasCurseForge reports whether CurseForge calls can be made.
func (s *Service) HasCurseForge() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CF.HasKey()
}

// Progress reports work; total may be 0 when unknown.
type Progress func(stage string, done, total int)

func (p Progress) report(stage string, done, total int) {
	if p != nil {
		p(stage, done, total)
	}
}

// Report collects non-fatal problems.
type Report struct {
	Warnings []string
}

func (r *Report) Warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// Enrichment

// Enrich fills in names, icons, sides, hashes and cross-platform references
// for mods that only carry IDs or hashes (as after an import). It never
// overrides a side the user locked.
func (s *Service) Enrich(ctx context.Context, p *model.Pack, prog Progress) Report {
	var rep Report
	total := 3
	prog.report("Looking up CurseForge files", 0, total)
	s.enrichCurseForge(ctx, p, &rep)
	if ctx.Err() != nil {
		return rep
	}
	prog.report("Matching files on Modrinth", 1, total)
	s.matchModrinthByHash(ctx, p, &rep)
	if ctx.Err() != nil {
		return rep
	}
	prog.report("Fetching Modrinth project details", 2, total)
	s.enrichModrinth(ctx, p, &rep)
	prog.report("Done", 3, total)
	return rep
}

func (s *Service) enrichCurseForge(ctx context.Context, p *model.Pack, rep *Report) {
	var fileIDs []int
	for _, m := range p.Mods {
		if m.CurseForge != nil && m.CurseForge.FileID != 0 {
			fileIDs = append(fileIDs, m.CurseForge.FileID)
		}
	}
	if len(fileIDs) == 0 {
		return
	}
	if !s.HasCurseForge() {
		rep.Warn("%d mods come from CurseForge. Add a CurseForge API key in Preferences to show their names and allow exporting them to other formats.", len(fileIDs))
		return
	}
	files, err := s.CF.Files(ctx, fileIDs)
	if err != nil {
		rep.Warn("CurseForge file lookup failed: %v", err)
		return
	}
	byID := map[int]*curseforge.File{}
	modIDs := map[int]bool{}
	for i := range files {
		byID[files[i].ID] = &files[i]
		modIDs[files[i].ModID] = true
	}
	var ids []int
	for id := range modIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	mods, err := s.CF.Mods(ctx, ids)
	if err != nil {
		rep.Warn("CurseForge project lookup failed: %v", err)
	}
	modByID := map[int]*curseforge.Mod{}
	for i := range mods {
		modByID[mods[i].ID] = &mods[i]
	}
	for _, m := range p.Mods {
		if m.CurseForge == nil {
			continue
		}
		f := byID[m.CurseForge.FileID]
		if f == nil {
			if m.CurseForge.FileID != 0 {
				rep.Warn("CurseForge file %d (%s) was not found; it may have been deleted.", m.CurseForge.FileID, m.DisplayName())
			}
			continue
		}
		m.CurseForge.ProjectID = f.ModID
		applyCFFile(m, f)
		if cm := modByID[f.ModID]; cm != nil {
			applyCFMod(m, cm)
		}
	}
}

func applyCFFile(m *model.Mod, f *curseforge.File) {
	folder := model.FolderForProjectType(m.ProjectType)
	if m.File.Path == "" || !strings.HasSuffix(m.File.Path, f.FileName) {
		m.File.Path = folder + "/" + f.FileName
	}
	if m.File.Size == 0 {
		m.File.Size = f.FileLength
	}
	if sha1 := f.Hash(curseforge.AlgoSHA1); sha1 != "" {
		m.File.SHA1 = sha1
	}
	if md5 := f.Hash(curseforge.AlgoMD5); md5 != "" {
		m.File.MD5 = md5
	}
	url := ""
	if f.DownloadURL != nil && *f.DownloadURL != "" {
		url = *f.DownloadURL
	} else {
		url = formats.ForgeCDNURL(f.ID, f.FileName)
	}
	if !containsString(m.File.URLs, url) {
		m.File.URLs = append(m.File.URLs, url)
	}
	if !m.SideLocked && (m.Side == model.SideUnknown || m.Side == "") {
		m.Side = sideFromCFFile(f)
	}
}

func sideFromCFFile(f *curseforge.File) model.Side {
	c, s := f.HasGameVersion("Client"), f.HasGameVersion("Server")
	switch {
	case c && s:
		return model.SideBoth
	case c:
		return model.SideClient
	case s:
		return model.SideServer
	}
	return model.SideUnknown
}

func applyCFMod(m *model.Mod, cm *curseforge.Mod) {
	m.Name = cm.Name
	m.Slug = cm.Slug
	m.Summary = cm.Summary
	m.Author = cm.AuthorNames()
	if icon := cm.IconURL(); icon != "" {
		m.IconURL = icon
	}
	switch cm.ClassID {
	case curseforge.ClassResource:
		m.ProjectType = "resourcepack"
	case curseforge.ClassShaders:
		m.ProjectType = "shader"
	}
	if m.ProjectType != "" && m.ProjectType != "mod" {
		folder := model.FolderForProjectType(m.ProjectType)
		if !strings.HasPrefix(m.File.Path, folder+"/") {
			m.File.Path = folder + "/" + m.File.Name()
		}
	}
}

func (s *Service) matchModrinthByHash(ctx context.Context, p *model.Pack, rep *Report) {
	var hashes []string
	for _, m := range p.Mods {
		if m.Modrinth == nil && m.File.SHA1 != "" {
			hashes = append(hashes, strings.ToLower(m.File.SHA1))
		}
	}
	if len(hashes) == 0 {
		return
	}
	found, err := s.MR.VersionsByHash(ctx, hashes, "sha1")
	if err != nil {
		rep.Warn("Modrinth hash lookup failed: %v", err)
		return
	}
	for _, m := range p.Mods {
		if m.Modrinth != nil || m.File.SHA1 == "" {
			continue
		}
		v, ok := found[strings.ToLower(m.File.SHA1)]
		if !ok {
			continue
		}
		m.Modrinth = &model.ModrinthRef{ProjectID: v.ProjectID, VersionID: v.ID, VersionNumber: v.VersionNumber}
		for _, f := range v.Files {
			if strings.EqualFold(f.Hashes["sha1"], m.File.SHA1) {
				if m.File.SHA512 == "" {
					m.File.SHA512 = f.Hashes["sha512"]
				}
				if !containsString(m.File.URLs, f.URL) {
					// Prefer Modrinth's CDN: it is accepted everywhere.
					m.File.URLs = append([]string{f.URL}, m.File.URLs...)
				}
			}
		}
	}
}

func (s *Service) enrichModrinth(ctx context.Context, p *model.Pack, rep *Report) {
	idSet := map[string]bool{}
	var versionIDs []string
	for _, m := range p.Mods {
		if m.Modrinth != nil && m.Modrinth.ProjectID != "" {
			idSet[m.Modrinth.ProjectID] = true
			if m.Modrinth.VersionID != "" {
				versionIDs = append(versionIDs, m.Modrinth.VersionID)
			}
		}
	}
	if len(idSet) == 0 {
		return
	}
	var ids []string
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	projects, err := s.MR.Projects(ctx, ids)
	if err != nil {
		rep.Warn("Modrinth project lookup failed: %v", err)
		return
	}
	byID := map[string]*modrinth.Project{}
	var teamIDs []string
	for i := range projects {
		byID[projects[i].ID] = &projects[i]
		if projects[i].Team != "" {
			teamIDs = append(teamIDs, projects[i].Team)
		}
	}
	owners := map[string]string{}
	if teams, err := s.MR.Teams(ctx, teamIDs); err == nil {
		for _, members := range teams {
			if len(members) > 0 {
				owners[members[0].TeamID] = modrinth.OwnerName(members)
			}
		}
	}
	envByVersion := map[string]modrinth.Version{}
	if len(versionIDs) > 0 {
		if vs, err := s.MR.Versions(ctx, versionIDs); err == nil {
			for _, v := range vs {
				envByVersion[v.ID] = v
			}
		}
	}
	for _, m := range p.Mods {
		if m.Modrinth == nil {
			continue
		}
		pr := byID[m.Modrinth.ProjectID]
		if pr == nil {
			continue
		}
		v, hasV := envByVersion[m.Modrinth.VersionID]
		if hasV && m.Modrinth.VersionNumber == "" {
			m.Modrinth.VersionNumber = v.VersionNumber
		}
		applyMRProject(m, pr, v.Environment)
		if a := owners[pr.Team]; a != "" {
			m.Author = a
		}
	}
}

// applyMRProject copies display metadata. environment is the version-level
// environment field (may be empty).
func applyMRProject(m *model.Mod, pr *modrinth.Project, environment string) {
	m.Name = pr.Title
	m.Slug = pr.Slug
	m.Summary = pr.Description
	if pr.IconURL != "" {
		m.IconURL = pr.IconURL
	}
	if pr.ProjectType != "" {
		m.ProjectType = pr.ProjectType
	}
	if m.SideLocked {
		return
	}
	// A one-sided env written by the pack author is deliberate; keep it.
	if m.Side == model.SideClient || m.Side == model.SideServer {
		if m.ClientEnv != "" || m.ServerEnv != "" {
			return
		}
	}
	if side, ok := model.SideFromModrinthEnvironment(environment); ok {
		m.Side = side
		return
	}
	if side := model.SideFromEnv(pr.ClientSide, pr.ServerSide); side != model.SideUnknown {
		m.Side = side
	}
	if m.ProjectType == "resourcepack" || m.ProjectType == "shader" {
		m.Side = model.SideClient
	}
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Adding mods

// AddResult describes what AddProject did.
type AddResult struct {
	Added    []*model.Mod
	Existing []*model.Mod // already present (including the requested one)
	Report
}

// ModrinthLoaders returns the loader filters for a pack loader.
func ModrinthLoaders(loader string) []string {
	switch loader {
	case "quilt":
		return []string{"quilt", "fabric"}
	case "":
		return nil
	}
	return []string{loader}
}

func pickModrinthVersion(vs []modrinth.Version) *modrinth.Version {
	if len(vs) == 0 {
		return nil
	}
	rank := map[string]int{"release": 0, "beta": 1, "alpha": 2}
	sort.SliceStable(vs, func(i, j int) bool {
		ri, rj := rank[vs[i].VersionType], rank[vs[j].VersionType]
		if ri != rj {
			return ri < rj
		}
		return vs[i].DatePublished.After(vs[j].DatePublished)
	})
	return &vs[0]
}

// AddProject adds a project (and its required dependencies) to the pack.
func (s *Service) AddProject(ctx context.Context, p *model.Pack, src model.Source, projectID, groupID string) (*AddResult, error) {
	res := &AddResult{}
	seen := map[string]bool{}
	var err error
	switch src {
	case model.SourceModrinth:
		err = s.addModrinth(ctx, p, projectID, "", groupID, res, seen, 0)
	case model.SourceCurseForge:
		id, convErr := strconv.Atoi(projectID)
		if convErr != nil {
			return nil, fmt.Errorf("invalid CurseForge project id %q", projectID)
		}
		err = s.addCurseForge(ctx, p, id, groupID, res, seen, 0)
	default:
		return nil, fmt.Errorf("unknown source %q", src)
	}
	return res, err
}

const maxDepDepth = 6

func (s *Service) addModrinth(ctx context.Context, p *model.Pack, projectID, versionID, groupID string, res *AddResult, seen map[string]bool, depth int) error {
	if seen["mr:"+projectID] || depth > maxDepDepth {
		return nil
	}
	seen["mr:"+projectID] = true
	pr, err := s.MR.Project(ctx, projectID)
	if err != nil {
		return fmt.Errorf("loading Modrinth project %s: %w", projectID, err)
	}
	seen["mr:"+pr.ID] = true
	seen["mr:"+pr.Slug] = true
	if ex := p.FindDuplicate(&model.Mod{Modrinth: &model.ModrinthRef{ProjectID: pr.ID}}); ex != nil {
		res.Existing = append(res.Existing, ex)
		return nil
	}
	var v *modrinth.Version
	if versionID != "" {
		v, err = s.MR.Version(ctx, versionID)
		if err != nil {
			v = nil
		}
	}
	if v == nil {
		var loaders []string
		if pr.ProjectType == "mod" || pr.ProjectType == "" {
			loaders = ModrinthLoaders(p.Loader)
		}
		var gv []string
		if p.Minecraft != "" {
			gv = []string{p.Minecraft}
		}
		vs, err := s.MR.ProjectVersions(ctx, pr.ID, loaders, gv)
		if err != nil {
			return fmt.Errorf("listing versions of %s: %w", pr.Title, err)
		}
		v = pickModrinthVersion(vs)
		if v == nil {
			return &NoCompatibleVersionError{Name: pr.Title, Minecraft: p.Minecraft, Loader: p.Loader}
		}
	}
	f := v.PrimaryFile()
	if f == nil {
		return fmt.Errorf("%s %s has no files", pr.Title, v.VersionNumber)
	}
	m := &model.Mod{
		Modrinth:    &model.ModrinthRef{ProjectID: pr.ID, VersionID: v.ID, VersionNumber: v.VersionNumber},
		ProjectType: pr.ProjectType,
		GroupID:     groupID,
		Side:        model.SideUnknown,
		File: model.File{
			Path:   model.FolderForProjectType(pr.ProjectType) + "/" + f.Filename,
			Size:   f.Size,
			SHA1:   f.Hashes["sha1"],
			SHA512: f.Hashes["sha512"],
			URLs:   []string{f.URL},
		},
	}
	applyMRProject(m, pr, v.Environment)
	if members, err := s.MR.Members(ctx, pr.ID); err == nil {
		m.Author = modrinth.OwnerName(members)
	}
	p.AddMod(m)
	res.Added = append(res.Added, m)
	for _, d := range v.Dependencies {
		switch d.DependencyType {
		case "required":
			pid := d.ProjectID
			if pid == "" && d.VersionID != "" {
				if dv, err := s.MR.Version(ctx, d.VersionID); err == nil {
					pid = dv.ProjectID
				}
			}
			if pid == "" {
				continue
			}
			if err := s.addModrinth(ctx, p, pid, "", groupID, res, seen, depth+1); err != nil {
				res.Warn("Dependency of %s: %v", pr.Title, err)
			}
		case "incompatible":
			if d.ProjectID != "" {
				if ex := p.FindDuplicate(&model.Mod{Modrinth: &model.ModrinthRef{ProjectID: d.ProjectID}}); ex != nil {
					res.Warn("%s is marked incompatible with %s, which is in the pack.", pr.Title, ex.DisplayName())
				}
			}
		}
	}
	return nil
}

// NoCompatibleVersionError means the project has no file for the pack's
// Minecraft version / loader.
type NoCompatibleVersionError struct {
	Name, Minecraft, Loader string
}

func (e *NoCompatibleVersionError) Error() string {
	return fmt.Sprintf("%s has no version for Minecraft %s with %s", e.Name, e.Minecraft, model.LoaderLabel(e.Loader))
}

// IsNoCompatibleVersion reports whether err is a NoCompatibleVersionError.
func IsNoCompatibleVersion(err error) bool {
	var e *NoCompatibleVersionError
	return errors.As(err, &e)
}

func pickCFFile(fs []curseforge.File, mc string) *curseforge.File {
	var c []curseforge.File
	for _, f := range fs {
		if mc != "" && !f.HasGameVersion(mc) {
			continue
		}
		c = append(c, f)
	}
	if len(c) == 0 {
		return nil
	}
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].ReleaseType != c[j].ReleaseType {
			return c[i].ReleaseType < c[j].ReleaseType
		}
		return c[i].FileDate.After(c[j].FileDate)
	})
	return &c[0]
}

func (s *Service) addCurseForge(ctx context.Context, p *model.Pack, modID int, groupID string, res *AddResult, seen map[string]bool, depth int) error {
	key := "cf:" + strconv.Itoa(modID)
	if seen[key] || depth > maxDepDepth {
		return nil
	}
	seen[key] = true
	if ex := p.FindDuplicate(&model.Mod{CurseForge: &model.CurseForgeRef{ProjectID: modID}}); ex != nil {
		res.Existing = append(res.Existing, ex)
		return nil
	}
	cm, err := s.CF.Mod(ctx, modID)
	if err != nil {
		return fmt.Errorf("loading CurseForge project %d: %w", modID, err)
	}
	loaderType := 0
	if cm.ClassID == curseforge.ClassMods || cm.ClassID == 0 {
		loaderType = curseforge.LoaderType(p.Loader)
	}
	files, err := s.CF.ModFiles(ctx, modID, p.Minecraft, loaderType)
	if err != nil {
		return fmt.Errorf("listing files of %s: %w", cm.Name, err)
	}
	f := pickCFFile(files, p.Minecraft)
	if f == nil {
		return &NoCompatibleVersionError{Name: cm.Name, Minecraft: p.Minecraft, Loader: p.Loader}
	}
	m := &model.Mod{
		CurseForge:  &model.CurseForgeRef{ProjectID: modID, FileID: f.ID, Required: true},
		ProjectType: "mod",
		GroupID:     groupID,
		Side:        model.SideUnknown,
	}
	applyCFMod(m, cm)
	applyCFFile(m, f)
	// Best effort: cross-reference on Modrinth for side data and hashes.
	tmp := &model.Pack{Mods: []*model.Mod{m}}
	var rep Report
	s.matchModrinthByHash(ctx, tmp, &rep)
	if m.Modrinth != nil {
		s.enrichModrinthSideOnly(ctx, m)
	}
	p.AddMod(m)
	res.Added = append(res.Added, m)
	for _, d := range f.Dependencies {
		switch d.RelationType {
		case curseforge.RelRequired:
			if err := s.addCurseForge(ctx, p, d.ModID, groupID, res, seen, depth+1); err != nil {
				res.Warn("Dependency of %s: %v", cm.Name, err)
			}
		case curseforge.RelIncompatible:
			if ex := p.FindDuplicate(&model.Mod{CurseForge: &model.CurseForgeRef{ProjectID: d.ModID}}); ex != nil {
				res.Warn("%s is marked incompatible with %s, which is in the pack.", cm.Name, ex.DisplayName())
			}
		}
	}
	return nil
}

// enrichModrinthSideOnly updates only the side of a CurseForge mod that was
// matched on Modrinth, keeping CurseForge's display metadata.
func (s *Service) enrichModrinthSideOnly(ctx context.Context, m *model.Mod) {
	if m.SideLocked || m.Modrinth == nil {
		return
	}
	pr, err := s.MR.Project(ctx, m.Modrinth.ProjectID)
	if err != nil {
		return
	}
	env := ""
	if m.Modrinth.VersionID != "" {
		if v, err := s.MR.Version(ctx, m.Modrinth.VersionID); err == nil {
			env = v.Environment
		}
	}
	if side, ok := model.SideFromModrinthEnvironment(env); ok {
		m.Side = side
	} else if side := model.SideFromEnv(pr.ClientSide, pr.ServerSide); side != model.SideUnknown {
		m.Side = side
	}
}
