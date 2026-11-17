package formats

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"modpackmanager/internal/model"
)

// CFManifest mirrors a CurseForge modpack manifest.json.
type CFManifest struct {
	Minecraft       CFMinecraft `json:"minecraft"`
	ManifestType    string      `json:"manifestType"`
	ManifestVersion int         `json:"manifestVersion"`
	Name            string      `json:"name"`
	Version         string      `json:"version"`
	Author          string      `json:"author"`
	Files           []CFFile    `json:"files"`
	Overrides       string      `json:"overrides"`
}

type CFMinecraft struct {
	Version    string        `json:"version"`
	ModLoaders []CFModLoader `json:"modLoaders"`
}

type CFModLoader struct {
	ID      string `json:"id"`
	Primary bool   `json:"primary"`
}

type CFFile struct {
	ProjectID int  `json:"projectID"`
	FileID    int  `json:"fileID"`
	Required  bool `json:"required"`
}

// ParseCFLoader splits "forge-47.2.0" into ("forge", "47.2.0").
func ParseCFLoader(id string) (loader, version string) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, l := range []string{"neoforge", "forge", "fabric", "quilt"} {
		if strings.HasPrefix(id, l+"-") {
			return l, strings.TrimPrefix(id, l+"-")
		}
	}
	return "", ""
}

// ReadCurseForge parses a CurseForge modpack zip. Mod names, hashes and
// download URLs are not part of the manifest; the caller enriches them via the
// CurseForge API.
func ReadCurseForge(zr *zip.Reader) (*model.Pack, error) {
	f := findFile(zr, "manifest.json")
	if f == nil {
		return nil, fmt.Errorf("not a CurseForge pack: manifest.json missing")
	}
	var man CFManifest
	if err := readJSON(f, &man); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if man.ManifestType != "" && man.ManifestType != "minecraftModpack" {
		return nil, fmt.Errorf("unsupported manifestType %q", man.ManifestType)
	}
	p := &model.Pack{
		Name:      man.Name,
		Version:   man.Version,
		Author:    man.Author,
		Minecraft: man.Minecraft.Version,
	}
	for i, l := range man.Minecraft.ModLoaders {
		if l.Primary || i == 0 {
			p.Loader, p.LoaderVersion = ParseCFLoader(l.ID)
		}
	}
	for _, cf := range man.Files {
		m := &model.Mod{
			CurseForge:  &model.CurseForgeRef{ProjectID: cf.ProjectID, FileID: cf.FileID, Required: cf.Required},
			Side:        model.SideUnknown,
			ProjectType: "mod",
			Name:        fmt.Sprintf("CurseForge project %d", cf.ProjectID),
		}
		p.AddMod(m)
	}
	dir := strings.Trim(man.Overrides, "/")
	if dir == "" {
		dir = "overrides"
	}
	ov, err := readOverrides(zr, map[string]model.Side{dir + "/": model.SideBoth})
	if err != nil {
		return nil, err
	}
	p.Overrides = ov
	return p, nil
}

// CFLoaderID builds the manifest loader id, e.g. "fabric-0.16.9".
func CFLoaderID(loader, version string) string {
	if loader == "" || version == "" {
		return ""
	}
	return loader + "-" + version
}

// CurseForgeWriteOptions controls WriteCurseForge.
type CurseForgeWriteOptions struct {
	// Embedded holds jar bytes for mods that are not on CurseForge; they are
	// placed in overrides/<path>.
	Embedded map[string][]byte
}

// WriteCurseForge writes a CurseForge-compatible modpack zip. Mods without a
// CurseForge reference must be supplied in opt.Embedded.
func WriteCurseForge(w io.Writer, p *model.Pack, mods []*model.Mod, overrides []*model.OverrideFile, opt CurseForgeWriteOptions) error {
	man := CFManifest{
		Minecraft:       CFMinecraft{Version: p.Minecraft, ModLoaders: []CFModLoader{}},
		ManifestType:    "minecraftModpack",
		ManifestVersion: 1,
		Name:            p.Name,
		Version:         p.Version,
		Author:          p.Author,
		Files:           []CFFile{},
		Overrides:       "overrides",
	}
	if id := CFLoaderID(p.Loader, p.LoaderVersion); id != "" {
		man.Minecraft.ModLoaders = append(man.Minecraft.ModLoaders, CFModLoader{ID: id, Primary: true})
	}
	zw := zip.NewWriter(w)
	var list strings.Builder
	list.WriteString("<ul>\n")
	for _, m := range mods {
		if data, ok := opt.Embedded[m.ID]; ok {
			if err := writeZipFile(zw, "overrides/"+m.File.Path, data); err != nil {
				return err
			}
			continue
		}
		if m.CurseForge == nil || m.CurseForge.ProjectID == 0 || m.CurseForge.FileID == 0 {
			return fmt.Errorf("%s is not on CurseForge and was not embedded", m.DisplayName())
		}
		man.Files = append(man.Files, CFFile{ProjectID: m.CurseForge.ProjectID, FileID: m.CurseForge.FileID, Required: true})
		link := m.WebURL()
		if link == "" {
			link = fmt.Sprintf("https://www.curseforge.com/projects/%d", m.CurseForge.ProjectID)
		}
		author := ""
		if m.Author != "" {
			author = " (by " + html.EscapeString(m.Author) + ")"
		}
		fmt.Fprintf(&list, "<li><a href=\"%s\">%s%s</a></li>\n", html.EscapeString(link), html.EscapeString(m.DisplayName()), author)
	}
	list.WriteString("</ul>\n")
	for _, o := range overrides {
		if err := writeZipFile(zw, "overrides/"+o.Path, o.Data); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipFile(zw, "manifest.json", data); err != nil {
		return err
	}
	if err := writeZipFile(zw, "modlist.html", []byte(list.String())); err != nil {
		return err
	}
	return zw.Close()
}
