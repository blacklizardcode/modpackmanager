// Package curseforge is a minimal client for the CurseForge Core API (v1).
// Every request needs an API key from https://console.curseforge.com/.
package curseforge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"modpackmanager/internal/api/httpx"
)

const (
	DefaultBaseURL = "https://api.curseforge.com"
	GameMinecraft  = 432
	ClassMods      = 6
	ClassResource  = 12
	ClassShaders   = 6552
	ClassModpacks  = 4471
)

// ErrNoAPIKey is returned when no key is configured.
var ErrNoAPIKey = errors.New("no CurseForge API key configured (Preferences → CurseForge API key)")

// Mod loader type ids used by the API.
const (
	LoaderAny      = 0
	LoaderForge    = 1
	LoaderFabric   = 4
	LoaderQuilt    = 5
	LoaderNeoForge = 6
)

// LoaderType maps a loader name to the API's enum.
func LoaderType(name string) int {
	switch name {
	case "forge":
		return LoaderForge
	case "fabric":
		return LoaderFabric
	case "quilt":
		return LoaderQuilt
	case "neoforge":
		return LoaderNeoForge
	}
	return LoaderAny
}

// Relation types of file dependencies.
const (
	RelEmbedded     = 1
	RelOptional     = 2
	RelRequired     = 3
	RelTool         = 4
	RelIncompatible = 5
	RelInclude      = 6
)

// Hash algorithms.
const (
	AlgoSHA1 = 1
	AlgoMD5  = 2
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(apiKey string) *Client {
	return &Client{BaseURL: DefaultBaseURL, APIKey: apiKey, HTTP: httpx.DefaultClient}
}

// HasKey reports whether requests can be made.
func (c *Client) HasKey() bool { return c != nil && strings.TrimSpace(c.APIKey) != "" }

func (c *Client) do(ctx context.Context, method, p string, q url.Values, body, out any) error {
	if !c.HasKey() {
		return ErrNoAPIKey
	}
	u := strings.TrimRight(c.BaseURL, "/") + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return httpx.DoJSON(ctx, c.HTTP, method, u, map[string]string{"x-api-key": strings.TrimSpace(c.APIKey)}, body, out)
}

type Author struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Logo struct {
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type Category struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Links struct {
	WebsiteURL string `json:"websiteUrl"`
	WikiURL    string `json:"wikiUrl"`
	IssuesURL  string `json:"issuesUrl"`
	SourceURL  string `json:"sourceUrl"`
}

type FileIndex struct {
	GameVersion string `json:"gameVersion"`
	FileID      int    `json:"fileId"`
	Filename    string `json:"filename"`
	ReleaseType int    `json:"releaseType"`
	ModLoader   *int   `json:"modLoader"`
}

type Mod struct {
	ID                   int         `json:"id"`
	GameID               int         `json:"gameId"`
	Name                 string      `json:"name"`
	Slug                 string      `json:"slug"`
	Links                Links       `json:"links"`
	Summary              string      `json:"summary"`
	DownloadCount        float64     `json:"downloadCount"`
	ClassID              int         `json:"classId"`
	Categories           []Category  `json:"categories"`
	Authors              []Author    `json:"authors"`
	Logo                 *Logo       `json:"logo"`
	DateModified         time.Time   `json:"dateModified"`
	LatestFilesIndexes   []FileIndex `json:"latestFilesIndexes"`
	AllowModDistribution *bool       `json:"allowModDistribution"`
}

// AuthorNames joins the author list.
func (m *Mod) AuthorNames() string {
	var n []string
	for _, a := range m.Authors {
		n = append(n, a.Name)
	}
	return strings.Join(n, ", ")
}

// IconURL returns the thumbnail if available.
func (m *Mod) IconURL() string {
	if m.Logo == nil {
		return ""
	}
	if m.Logo.ThumbnailURL != "" {
		return m.Logo.ThumbnailURL
	}
	return m.Logo.URL
}

type FileHash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

type FileDependency struct {
	ModID        int `json:"modId"`
	RelationType int `json:"relationType"`
}

type File struct {
	ID           int              `json:"id"`
	ModID        int              `json:"modId"`
	DisplayName  string           `json:"displayName"`
	FileName     string           `json:"fileName"`
	ReleaseType  int              `json:"releaseType"` // 1 release, 2 beta, 3 alpha
	FileDate     time.Time        `json:"fileDate"`
	FileLength   int64            `json:"fileLength"`
	DownloadURL  *string          `json:"downloadUrl"`
	GameVersions []string         `json:"gameVersions"`
	Hashes       []FileHash       `json:"hashes"`
	Dependencies []FileDependency `json:"dependencies"`
	IsAvailable  bool             `json:"isAvailable"`
}

// Hash returns the hash for an algorithm, or "".
func (f *File) Hash(algo int) string {
	for _, h := range f.Hashes {
		if h.Algo == algo {
			return strings.ToLower(h.Value)
		}
	}
	return ""
}

// HasGameVersion reports whether a tag (version or "Client"/"Server"/loader) is present.
func (f *File) HasGameVersion(v string) bool {
	for _, g := range f.GameVersions {
		if strings.EqualFold(g, v) {
			return true
		}
	}
	return false
}

type Pagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

// SearchParams for SearchMods.
type SearchParams struct {
	Query       string
	ClassID     int
	GameVersion string
	LoaderType  int
	SortField   int // 1 featured, 2 popularity, 3 last updated, 4 name, 5 author, 6 total downloads
	Index       int
	PageSize    int
}

// Sort fields.
const (
	SortFeatured   = 1
	SortPopularity = 2
	SortUpdated    = 3
	SortName       = 4
	SortDownloads  = 6
)

func (c *Client) SearchMods(ctx context.Context, p SearchParams) ([]Mod, Pagination, error) {
	q := url.Values{}
	q.Set("gameId", strconv.Itoa(GameMinecraft))
	if p.ClassID != 0 {
		q.Set("classId", strconv.Itoa(p.ClassID))
	}
	if p.Query != "" {
		q.Set("searchFilter", p.Query)
	}
	if p.GameVersion != "" {
		q.Set("gameVersion", p.GameVersion)
	}
	if p.LoaderType != 0 {
		q.Set("modLoaderType", strconv.Itoa(p.LoaderType))
	}
	if p.SortField != 0 {
		q.Set("sortField", strconv.Itoa(p.SortField))
		q.Set("sortOrder", "desc")
		if p.SortField == SortName {
			q.Set("sortOrder", "asc")
		}
	}
	if p.PageSize <= 0 {
		p.PageSize = 20
	}
	q.Set("pageSize", strconv.Itoa(p.PageSize))
	q.Set("index", strconv.Itoa(p.Index))
	var out struct {
		Data       []Mod      `json:"data"`
		Pagination Pagination `json:"pagination"`
	}
	err := c.do(ctx, "GET", "/v1/mods/search", q, nil, &out)
	return out.Data, out.Pagination, err
}

func (c *Client) Mod(ctx context.Context, id int) (*Mod, error) {
	var out struct {
		Data Mod `json:"data"`
	}
	err := c.do(ctx, "GET", "/v1/mods/"+strconv.Itoa(id), nil, nil, &out)
	return &out.Data, err
}

// Mods fetches several mods by ID.
func (c *Client) Mods(ctx context.Context, ids []int) ([]Mod, error) {
	var all []Mod
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		var out struct {
			Data []Mod `json:"data"`
		}
		if err := c.do(ctx, "POST", "/v1/mods", nil, map[string]any{"modIds": ids[start:end]}, &out); err != nil {
			return all, err
		}
		all = append(all, out.Data...)
	}
	return all, nil
}

// Files fetches several files by ID.
func (c *Client) Files(ctx context.Context, ids []int) ([]File, error) {
	var all []File
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		var out struct {
			Data []File `json:"data"`
		}
		if err := c.do(ctx, "POST", "/v1/mods/files", nil, map[string]any{"fileIds": ids[start:end]}, &out); err != nil {
			return all, err
		}
		all = append(all, out.Data...)
	}
	return all, nil
}

// ModFiles lists files of a mod filtered by game version / loader.
func (c *Client) ModFiles(ctx context.Context, modID int, gameVersion string, loaderType int) ([]File, error) {
	q := url.Values{}
	if gameVersion != "" {
		q.Set("gameVersion", gameVersion)
	}
	if loaderType != 0 {
		q.Set("modLoaderType", strconv.Itoa(loaderType))
	}
	q.Set("pageSize", "50")
	var out struct {
		Data []File `json:"data"`
	}
	err := c.do(ctx, "GET", "/v1/mods/"+strconv.Itoa(modID)+"/files", q, nil, &out)
	return out.Data, err
}

// Description returns the mod's HTML description.
func (c *Client) Description(ctx context.Context, modID int) (string, error) {
	var out struct {
		Data string `json:"data"`
	}
	err := c.do(ctx, "GET", "/v1/mods/"+strconv.Itoa(modID)+"/description", nil, nil, &out)
	return out.Data, err
}

// FingerprintMatches is not used: CurseForge's murmur2 fingerprints need the
// jar bytes, which a modpack manifest doesn't carry.
