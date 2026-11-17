package core

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/api/modrinth"
	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

// SearchRequest is a unified online search.
type SearchRequest struct {
	Query       string
	Sources     []model.Source // empty = Modrinth
	Minecraft   string
	Loader      string
	ProjectType string // mod (default), resourcepack, shader
	Sort        string // relevance (default), downloads, updated
	Page        int
	PageSize    int
}

// Hit is one search result.
type Hit struct {
	Source      model.Source
	ID          string // Modrinth project id or CurseForge mod id
	Slug        string
	Title       string
	Summary     string
	Author      string
	IconURL     string
	Downloads   int64
	Updated     time.Time
	Side        model.Side
	Categories  []string
	ProjectType string
	// AlsoOn is set when the same project was found on the other platform.
	AlsoOn   model.Source
	AlsoOnID string
	score    float64
}

// Key uniquely identifies the hit.
func (h *Hit) Key() string { return string(h.Source) + ":" + h.ID }

// WebURL returns the project page.
func (h *Hit) WebURL() string {
	if h.Source == model.SourceModrinth {
		return "https://modrinth.com/project/" + h.Slug
	}
	return "https://www.curseforge.com/minecraft/" + cfClassPath(h.ProjectType) + "/" + h.Slug
}

func cfClassPath(t string) string {
	switch t {
	case "resourcepack":
		return "texture-packs"
	case "shader":
		return "shaders"
	}
	return "mc-mods"
}

// SearchResult holds merged hits and per-source errors.
type SearchResult struct {
	Hits    []*Hit
	Total   int
	Errors  map[model.Source]error
	HasMore bool
}

// Search runs the query against each source concurrently and merges results.
func (s *Service) Search(ctx context.Context, r SearchRequest) *SearchResult {
	if len(r.Sources) == 0 {
		r.Sources = []model.Source{model.SourceModrinth}
	}
	if r.PageSize <= 0 {
		r.PageSize = 20
	}
	if r.ProjectType == "" {
		r.ProjectType = "mod"
	}
	type part struct {
		src   model.Source
		hits  []*Hit
		total int
		err   error
	}
	var wg sync.WaitGroup
	parts := make([]part, len(r.Sources))
	for i, src := range r.Sources {
		wg.Add(1)
		go func(i int, src model.Source) {
			defer wg.Done()
			var pt part
			pt.src = src
			switch src {
			case model.SourceModrinth:
				pt.hits, pt.total, pt.err = s.searchModrinth(ctx, r)
			case model.SourceCurseForge:
				pt.hits, pt.total, pt.err = s.searchCurseForge(ctx, r)
			}
			parts[i] = pt
		}(i, src)
	}
	wg.Wait()
	res := &SearchResult{Errors: map[model.Source]error{}}
	var lists [][]*Hit
	for _, pt := range parts {
		if pt.err != nil {
			res.Errors[pt.src] = pt.err
			continue
		}
		res.Total += pt.total
		if (r.Page+1)*r.PageSize < pt.total {
			res.HasMore = true
		}
		lists = append(lists, pt.hits)
	}
	res.Hits = Merge(r.Query, r.Sort, lists...)
	return res
}

func (s *Service) searchModrinth(ctx context.Context, r SearchRequest) ([]*Hit, int, error) {
	idx := "relevance"
	switch r.Sort {
	case "downloads":
		idx = "downloads"
	case "updated":
		idx = "updated"
	}
	var loaders []string
	if r.ProjectType == "mod" {
		loaders = ModrinthLoaders(r.Loader)
	}
	out, err := s.MR.Search(ctx, modrinth.SearchParams{
		Query:       r.Query,
		ProjectType: r.ProjectType,
		GameVersion: r.Minecraft,
		Loaders:     loaders,
		Index:       idx,
		Offset:      r.Page * r.PageSize,
		Limit:       r.PageSize,
	})
	if err != nil {
		return nil, 0, err
	}
	var hits []*Hit
	for _, h := range out.Hits {
		t, _ := time.Parse(time.RFC3339, h.DateModified)
		side := model.SideFromEnv(h.ClientSide, h.ServerSide)
		hits = append(hits, &Hit{
			Source: model.SourceModrinth, ID: h.ProjectID, Slug: h.Slug, Title: h.Title,
			Summary: h.Description, Author: h.Author, IconURL: h.IconURL, Downloads: h.Downloads,
			Updated: t, Side: side, Categories: h.Categories, ProjectType: h.ProjectType,
		})
	}
	return hits, out.TotalHits, nil
}

func (s *Service) searchCurseForge(ctx context.Context, r SearchRequest) ([]*Hit, int, error) {
	class := curseforge.ClassMods
	switch r.ProjectType {
	case "resourcepack":
		class = curseforge.ClassResource
	case "shader":
		class = curseforge.ClassShaders
	}
	sortField := curseforge.SortPopularity
	if strings.TrimSpace(r.Query) != "" {
		// CurseForge has no "relevance" sort; popularity with a filter is the
		// closest, and Merge re-ranks by text match afterwards.
		sortField = curseforge.SortPopularity
	}
	switch r.Sort {
	case "downloads":
		sortField = curseforge.SortDownloads
	case "updated":
		sortField = curseforge.SortUpdated
	}
	loaderType := 0
	if class == curseforge.ClassMods {
		loaderType = curseforge.LoaderType(r.Loader)
	}
	mods, pg, err := s.CF.SearchMods(ctx, curseforge.SearchParams{
		Query: r.Query, ClassID: class, GameVersion: r.Minecraft, LoaderType: loaderType,
		SortField: sortField, Index: r.Page * r.PageSize, PageSize: r.PageSize,
	})
	if err != nil {
		return nil, 0, err
	}
	var hits []*Hit
	for i := range mods {
		m := &mods[i]
		var cats []string
		for _, c := range m.Categories {
			cats = append(cats, c.Name)
		}
		hits = append(hits, &Hit{
			Source: model.SourceCurseForge, ID: strconv.Itoa(m.ID), Slug: m.Slug, Title: m.Name,
			Summary: m.Summary, Author: m.AuthorNames(), IconURL: m.IconURL(), Downloads: int64(m.DownloadCount),
			Updated: m.DateModified, Side: model.SideUnknown, Categories: cats, ProjectType: r.ProjectType,
		})
	}
	total := pg.TotalCount
	if total > 10000 {
		total = 10000 // CurseForge refuses index+pageSize > 10000
	}
	return hits, total, nil
}

// normTitle folds a title for cross-platform duplicate detection.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TextScore rates how well a hit's text matches the query terms (0..1+).
// Summaries are stripped of Markdown first so formatting characters never
// hide or fake a match.
func TextScore(query, title, slug, summary string) float64 {
	terms := markup.Terms(query)
	if len(terms) == 0 {
		return 0
	}
	t := strings.ToLower(title)
	nt := normTitle(title)
	nq := normTitle(query)
	sum := strings.ToLower(markup.MarkdownToPlain(summary))
	score := 0.0
	if nq != "" && nt == nq {
		score += 3
	} else if nq != "" && strings.HasPrefix(nt, nq) {
		score += 1.5
	}
	if nq != "" && normTitle(slug) == nq {
		score += 1
	}
	for _, term := range terms {
		switch {
		case hasWordPrefix(t, term):
			score += 1
		case strings.Contains(t, term):
			score += 0.6
		case strings.Contains(strings.ToLower(slug), term):
			score += 0.4
		case strings.Contains(sum, term):
			score += 0.25
		}
	}
	return score / float64(len(terms))
}

func hasWordPrefix(s, term string) bool {
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.HasPrefix(w, term) {
			return true
		}
	}
	return false
}

// Merge combines per-source result lists: duplicates across platforms are
// folded into one hit (Modrinth preferred), and the list is re-ranked by a
// blend of each platform's own rank and the text match.
func Merge(query, sortBy string, lists ...[]*Hit) []*Hit {
	var all []*Hit
	byTitle := map[string]*Hit{}
	for _, list := range lists {
		n := len(list)
		for i, h := range list {
			rankScore := 1.0
			if n > 1 {
				rankScore = 1 - float64(i)/float64(n)
			}
			h.score = rankScore + TextScore(query, h.Title, h.Slug, h.Summary)
			k := normTitle(h.Title)
			if prev, ok := byTitle[k]; ok && prev.Source != h.Source && k != "" {
				// Same project on both platforms: fold into one entry that
				// keeps its list position and prefers Modrinth's data.
				best := max(prev.score, h.score)
				if h.Source == model.SourceModrinth {
					cfID := prev.ID
					*prev = *h
					prev.AlsoOn, prev.AlsoOnID = model.SourceCurseForge, cfID
				} else {
					prev.AlsoOn, prev.AlsoOnID = h.Source, h.ID
				}
				prev.score = best
				continue
			}
			byTitle[k] = h
			all = append(all, h)
		}
	}
	switch sortBy {
	case "downloads":
		sort.SliceStable(all, func(i, j int) bool { return all[i].Downloads > all[j].Downloads })
	case "updated":
		sort.SliceStable(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
	default:
		if len(lists) > 1 || strings.TrimSpace(query) != "" {
			sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
		}
	}
	return all
}

// ---------------------------------------------------------------------------
// Details

// Details is the full description of a project for the detail pane.
type Details struct {
	Hit          *Hit
	Body         string // Markdown (Modrinth) or HTML (CurseForge)
	BodyIsHTML   bool
	License      string
	Links        [][2]string // label, url
	Gallery      []string
	GameVersions []string
	Loaders      []string
}

// Blocks renders the body for a dialect.
func (d *Details) Blocks(dialect markup.Dialect) []markup.Block {
	if d.BodyIsHTML {
		return markup.FromHTML(d.Body, dialect)
	}
	return markup.FromMarkdown(d.Body, dialect)
}

// ProjectDetails loads the long description and links of a hit.
func (s *Service) ProjectDetails(ctx context.Context, h *Hit) (*Details, error) {
	d := &Details{Hit: h}
	switch h.Source {
	case model.SourceModrinth:
		pr, err := s.MR.Project(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		d.Body = pr.Body
		d.License = pr.License.Name
		if d.License == "" {
			d.License = pr.License.ID
		}
		d.GameVersions, d.Loaders = pr.GameVersions, pr.Loaders
		add := func(label, u string) {
			if u != "" {
				d.Links = append(d.Links, [2]string{label, u})
			}
		}
		add("Modrinth page", "https://modrinth.com/project/"+pr.Slug)
		add("Source", pr.SourceURL)
		add("Issues", pr.IssuesURL)
		add("Wiki", pr.WikiURL)
		add("Discord", pr.DiscordURL)
		for _, g := range pr.Gallery {
			d.Gallery = append(d.Gallery, g.URL)
		}
		if h.Side == model.SideUnknown || h.Side == "" {
			h.Side = model.SideFromEnv(pr.ClientSide, pr.ServerSide)
		}
	case model.SourceCurseForge:
		id, err := strconv.Atoi(h.ID)
		if err != nil {
			return nil, err
		}
		body, err := s.CF.Description(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Body, d.BodyIsHTML = body, true
		d.Links = append(d.Links, [2]string{"CurseForge page", h.WebURL()})
		if m, err := s.CF.Mod(ctx, id); err == nil {
			if m.Links.SourceURL != "" {
				d.Links = append(d.Links, [2]string{"Source", m.Links.SourceURL})
			}
			if m.Links.IssuesURL != "" {
				d.Links = append(d.Links, [2]string{"Issues", m.Links.IssuesURL})
			}
			if m.Links.WikiURL != "" {
				d.Links = append(d.Links, [2]string{"Wiki", m.Links.WikiURL})
			}
		}
	}
	return d, nil
}

// HitFromMod builds a Hit for showing an installed mod's details.
func HitFromMod(m *model.Mod) *Hit {
	h := &Hit{Title: m.DisplayName(), Summary: m.Summary, Author: m.Author, IconURL: m.IconURL, Slug: m.Slug, Side: m.Side, ProjectType: m.ProjectType}
	switch {
	case m.Modrinth != nil:
		h.Source, h.ID = model.SourceModrinth, m.Modrinth.ProjectID
	case m.CurseForge != nil && m.CurseForge.ProjectID != 0:
		h.Source, h.ID = model.SourceCurseForge, strconv.Itoa(m.CurseForge.ProjectID)
	default:
		return nil
	}
	return h
}
