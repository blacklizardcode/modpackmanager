package formats

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"modpackmanager/internal/model"
)

// MrpackIndex mirrors modrinth.index.json (format version 1).
type MrpackIndex struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary,omitempty"`
	Files         []MrpackFile      `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

type MrpackFile struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       *MrpackEnv        `json:"env,omitempty"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
}

type MrpackEnv struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

// mrpack dependency keys for each loader.
var mrpackLoaderKeys = map[string]string{
	"fabric":   "fabric-loader",
	"forge":    "forge",
	"neoforge": "neoforge",
	"quilt":    "quilt-loader",
}

// MrpackLoaderKey returns the dependency key for a loader.
func MrpackLoaderKey(loader string) string { return mrpackLoaderKeys[loader] }

// ReadMrpack parses an .mrpack archive.
func ReadMrpack(zr *zip.Reader) (*model.Pack, error) {
	f := findFile(zr, "modrinth.index.json")
	if f == nil {
		return nil, fmt.Errorf("not an .mrpack: modrinth.index.json missing")
	}
	var idx MrpackIndex
	if err := readJSON(f, &idx); err != nil {
		return nil, fmt.Errorf("modrinth.index.json: %w", err)
	}
	if idx.FormatVersion != 1 {
		return nil, fmt.Errorf("unsupported mrpack formatVersion %d", idx.FormatVersion)
	}
	p := &model.Pack{
		Name:      idx.Name,
		Version:   idx.VersionID,
		Summary:   idx.Summary,
		Minecraft: idx.Dependencies["minecraft"],
	}
	for loader, key := range mrpackLoaderKeys {
		if v, ok := idx.Dependencies[key]; ok {
			p.Loader, p.LoaderVersion = loader, v
		}
	}
	for _, mf := range idx.Files {
		if err := SafeRelPath(mf.Path); err != nil {
			return nil, fmt.Errorf("file %q: %w", mf.Path, err)
		}
		m := &model.Mod{
			File: model.File{
				Path:   mf.Path,
				Size:   mf.FileSize,
				SHA1:   mf.Hashes["sha1"],
				SHA512: mf.Hashes["sha512"],
				URLs:   append([]string(nil), mf.Downloads...),
			},
			ProjectType: model.ProjectTypeFromPath(mf.Path),
		}
		if mf.Env != nil {
			m.ClientEnv, m.ServerEnv = mf.Env.Client, mf.Env.Server
			m.Side = model.SideFromEnv(mf.Env.Client, mf.Env.Server)
		} else {
			m.Side = model.SideUnknown
		}
		for _, u := range mf.Downloads {
			if pid, vid, ok := ParseModrinthCDN(u); ok {
				m.Modrinth = &model.ModrinthRef{ProjectID: pid, VersionID: vid}
				break
			}
			if fid, ok := ParseForgeCDN(u); ok && m.CurseForge == nil {
				m.CurseForge = &model.CurseForgeRef{FileID: fid, Required: true}
			}
		}
		p.AddMod(m)
	}
	ov, err := readOverrides(zr, map[string]model.Side{
		"overrides/":        model.SideBoth,
		"client-overrides/": model.SideClient,
		"server-overrides/": model.SideServer,
	})
	if err != nil {
		return nil, err
	}
	p.Overrides = ov
	promoteJars(p)
	return p, nil
}

// ParseModrinthCDN extracts IDs from https://cdn.modrinth.com/data/{project}/versions/{version}/{file}.
func ParseModrinthCDN(raw string) (projectID, versionID string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "cdn.modrinth.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "data" && parts[2] == "versions" {
		return parts[1], parts[3], true
	}
	return "", "", false
}

// ParseForgeCDN extracts the file ID from https://edge.forgecdn.net/files/{a}/{b}/{name}
// where fileID = a*1000 + b.
func ParseForgeCDN(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Host), "forgecdn.net") {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "files" {
		var a, b int
		if _, err := fmt.Sscanf(parts[1], "%d", &a); err != nil {
			return 0, false
		}
		if _, err := fmt.Sscanf(parts[2], "%d", &b); err != nil {
			return 0, false
		}
		return a*1000 + b, true
	}
	return 0, false
}

// ForgeCDNURL builds the conventional CDN URL for a CurseForge file.
func ForgeCDNURL(fileID int, fileName string) string {
	return fmt.Sprintf("https://edge.forgecdn.net/files/%d/%d/%s", fileID/1000, fileID%1000, url.PathEscape(fileName))
}

// MrpackWriteOptions controls WriteMrpack.
type MrpackWriteOptions struct {
	// Embedded holds jar bytes for mods that have no usable download URL and
	// should be shipped inside overrides instead (keyed by mod ID).
	Embedded map[string][]byte
}

// WriteMrpack writes the given mods and overrides as an .mrpack.
func WriteMrpack(w io.Writer, p *model.Pack, mods []*model.Mod, overrides []*model.OverrideFile, opt MrpackWriteOptions) error {
	idx := MrpackIndex{
		FormatVersion: 1,
		Game:          "minecraft",
		VersionID:     p.Version,
		Name:          p.Name,
		Summary:       p.Summary,
		Files:         []MrpackFile{},
		Dependencies:  map[string]string{"minecraft": p.Minecraft},
	}
	if key := MrpackLoaderKey(p.Loader); key != "" && p.LoaderVersion != "" {
		idx.Dependencies[key] = p.LoaderVersion
	}
	zw := zip.NewWriter(w)
	for _, m := range mods {
		if data, ok := opt.Embedded[m.ID]; ok {
			if err := writeZipFile(zw, overridePrefix(m.EffectiveSide())+m.File.Path, data); err != nil {
				return err
			}
			continue
		}
		if len(m.File.URLs) == 0 || m.File.SHA1 == "" || m.File.SHA512 == "" {
			return fmt.Errorf("%s: missing download URL or hashes", m.DisplayName())
		}
		c, s := m.ClientEnv, m.ServerEnv
		if m.SideLocked || c == "" || s == "" || model.SideFromEnv(c, s) != m.EffectiveSide() {
			c, s = model.EnvForSide(m.EffectiveSide())
		}
		idx.Files = append(idx.Files, MrpackFile{
			Path:      m.File.Path,
			Hashes:    map[string]string{"sha1": m.File.SHA1, "sha512": m.File.SHA512},
			Env:       &MrpackEnv{Client: c, Server: s},
			Downloads: m.File.URLs,
			FileSize:  m.File.Size,
		})
	}
	for _, o := range overrides {
		if err := writeZipFile(zw, overridePrefix(o.Side)+o.Path, o.Data); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipFile(zw, "modrinth.index.json", data); err != nil {
		return err
	}
	return zw.Close()
}
