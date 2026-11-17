// Package modrinth is a minimal client for the Modrinth v2 API.
package modrinth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"modpackmanager/internal/api/httpx"
)

const DefaultBaseURL = "https://api.modrinth.com/v2"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New() *Client { return &Client{BaseURL: DefaultBaseURL, HTTP: httpx.DefaultClient} }

func (c *Client) url(p string, q url.Values) string {
	u := strings.TrimRight(c.BaseURL, "/") + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func jsonList(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// SearchHit is one entry of /search.
type SearchHit struct {
	ProjectID    string   `json:"project_id"`
	ProjectType  string   `json:"project_type"`
	Slug         string   `json:"slug"`
	Author       string   `json:"author"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Categories   []string `json:"categories"`
	Versions     []string `json:"versions"`
	Downloads    int64    `json:"downloads"`
	Follows      int64    `json:"follows"`
	IconURL      string   `json:"icon_url"`
	DateModified string   `json:"date_modified"`
	ClientSide   string   `json:"client_side"`
	ServerSide   string   `json:"server_side"`
	Gallery      []string `json:"gallery"`
}

type SearchResult struct {
	Hits      []SearchHit `json:"hits"`
	Offset    int         `json:"offset"`
	Limit     int         `json:"limit"`
	TotalHits int         `json:"total_hits"`
}

// SearchParams for Search. Empty fields are not filtered on.
type SearchParams struct {
	Query       string
	ProjectType string // mod, resourcepack, shader, datapack
	GameVersion string
	Loaders     []string // any of
	Index       string   // relevance, downloads, follows, newest, updated
	Offset      int
	Limit       int
}

// Search queries /search.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchResult, error) {
	q := url.Values{}
	q.Set("query", p.Query)
	var facets [][]string
	if p.ProjectType != "" {
		facets = append(facets, []string{"project_type:" + p.ProjectType})
	}
	if p.GameVersion != "" {
		facets = append(facets, []string{"versions:" + p.GameVersion})
	}
	if len(p.Loaders) > 0 {
		var or []string
		for _, l := range p.Loaders {
			or = append(or, "categories:"+l)
		}
		facets = append(facets, or)
	}
	if len(facets) > 0 {
		b, _ := json.Marshal(facets)
		q.Set("facets", string(b))
	}
	if p.Index != "" {
		q.Set("index", p.Index)
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}
	q.Set("limit", strconv.Itoa(p.Limit))
	q.Set("offset", strconv.Itoa(p.Offset))
	var out SearchResult
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/search", q), nil, nil, &out)
	return &out, err
}

// Project is /project/{id}.
type Project struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	ProjectType string   `json:"project_type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Categories  []string `json:"categories"`
	ClientSide  string   `json:"client_side"`
	ServerSide  string   `json:"server_side"`
	Downloads   int64    `json:"downloads"`
	Followers   int64    `json:"followers"`
	IconURL     string   `json:"icon_url"`
	Team        string   `json:"team"`
	Updated     string   `json:"updated"`
	License     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"license"`
	SourceURL    string   `json:"source_url"`
	IssuesURL    string   `json:"issues_url"`
	WikiURL      string   `json:"wiki_url"`
	DiscordURL   string   `json:"discord_url"`
	GameVersions []string `json:"game_versions"`
	Loaders      []string `json:"loaders"`
	Gallery      []struct {
		URL      string `json:"url"`
		Featured bool   `json:"featured"`
		Title    string `json:"title"`
	} `json:"gallery"`
}

func (c *Client) Project(ctx context.Context, idOrSlug string) (*Project, error) {
	var out Project
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/project/"+url.PathEscape(idOrSlug), nil), nil, nil, &out)
	return &out, err
}

// Projects fetches several projects at once (chunked).
func (c *Client) Projects(ctx context.Context, ids []string) ([]Project, error) {
	var all []Project
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		q := url.Values{}
		q.Set("ids", jsonList(ids[start:end]))
		var out []Project
		if err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/projects", q), nil, nil, &out); err != nil {
			return all, err
		}
		all = append(all, out...)
	}
	return all, nil
}

type VersionFile struct {
	Hashes   map[string]string `json:"hashes"`
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Size     int64             `json:"size"`
	FileType *string           `json:"file_type"`
}

type Dependency struct {
	VersionID      string `json:"version_id"`
	ProjectID      string `json:"project_id"`
	FileName       string `json:"file_name"`
	DependencyType string `json:"dependency_type"` // required, optional, incompatible, embedded
}

type Version struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	Name          string        `json:"name"`
	VersionNumber string        `json:"version_number"`
	VersionType   string        `json:"version_type"` // release, beta, alpha
	GameVersions  []string      `json:"game_versions"`
	Loaders       []string      `json:"loaders"`
	Environment   string        `json:"environment"`
	DatePublished time.Time     `json:"date_published"`
	Files         []VersionFile `json:"files"`
	Dependencies  []Dependency  `json:"dependencies"`
}

// PrimaryFile returns the version's primary file (or the first one).
func (v *Version) PrimaryFile() *VersionFile {
	for i := range v.Files {
		if v.Files[i].Primary {
			return &v.Files[i]
		}
	}
	if len(v.Files) > 0 {
		return &v.Files[0]
	}
	return nil
}

// ProjectVersions lists versions, optionally filtered.
func (c *Client) ProjectVersions(ctx context.Context, idOrSlug string, loaders, gameVersions []string) ([]Version, error) {
	q := url.Values{}
	if len(loaders) > 0 {
		q.Set("loaders", jsonList(loaders))
	}
	if len(gameVersions) > 0 {
		q.Set("game_versions", jsonList(gameVersions))
	}
	var out []Version
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/project/"+url.PathEscape(idOrSlug)+"/version", q), nil, nil, &out)
	return out, err
}

func (c *Client) Version(ctx context.Context, id string) (*Version, error) {
	var out Version
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/version/"+url.PathEscape(id), nil), nil, nil, &out)
	return &out, err
}

// Versions fetches several versions by ID.
func (c *Client) Versions(ctx context.Context, ids []string) ([]Version, error) {
	var all []Version
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		q := url.Values{}
		q.Set("ids", jsonList(ids[start:end]))
		var out []Version
		if err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/versions", q), nil, nil, &out); err != nil {
			return all, err
		}
		all = append(all, out...)
	}
	return all, nil
}

// VersionsByHash maps file hashes to versions (POST /version_files).
func (c *Client) VersionsByHash(ctx context.Context, hashes []string, algorithm string) (map[string]Version, error) {
	all := map[string]Version{}
	for start := 0; start < len(hashes); start += 500 {
		end := min(start+500, len(hashes))
		body := map[string]any{"hashes": hashes[start:end], "algorithm": algorithm}
		out := map[string]Version{}
		if err := httpx.DoJSON(ctx, c.HTTP, "POST", c.url("/version_files", nil), nil, body, &out); err != nil {
			return all, err
		}
		for k, v := range out {
			all[strings.ToLower(k)] = v
		}
	}
	return all, nil
}

// TeamMember is an entry of /project/{id}/members.
type TeamMember struct {
	TeamID string `json:"team_id"`
	Role   string `json:"role"`
	User   struct {
		Username string `json:"username"`
	} `json:"user"`
}

func (c *Client) Members(ctx context.Context, projectID string) ([]TeamMember, error) {
	var out []TeamMember
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/project/"+url.PathEscape(projectID)+"/members", nil), nil, nil, &out)
	return out, err
}

// Teams fetches the members of several teams (GET /teams).
func (c *Client) Teams(ctx context.Context, teamIDs []string) ([][]TeamMember, error) {
	var all [][]TeamMember
	for start := 0; start < len(teamIDs); start += 100 {
		end := min(start+100, len(teamIDs))
		q := url.Values{}
		q.Set("ids", jsonList(teamIDs[start:end]))
		var out [][]TeamMember
		if err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/teams", q), nil, nil, &out); err != nil {
			return all, err
		}
		all = append(all, out...)
	}
	return all, nil
}

// OwnerName picks the owner (or first member) of a team.
func OwnerName(members []TeamMember) string {
	for _, m := range members {
		if strings.EqualFold(m.Role, "owner") {
			return m.User.Username
		}
	}
	if len(members) > 0 {
		return members[0].User.Username
	}
	return ""
}

// GameVersionTag is an entry of /tag/game_version.
type GameVersionTag struct {
	Version     string    `json:"version"`
	VersionType string    `json:"version_type"`
	Date        time.Time `json:"date"`
	Major       bool      `json:"major"`
}

func (c *Client) GameVersions(ctx context.Context) ([]GameVersionTag, error) {
	var out []GameVersionTag
	err := httpx.DoJSON(ctx, c.HTTP, "GET", c.url("/tag/game_version", nil), nil, nil, &out)
	return out, err
}
