package core

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/api/modrinth"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

// fake jars served by the mock CDN
var jars = map[string][]byte{
	"sodium.jar":    []byte("sodium-bytes"),
	"fabricapi.jar": []byte("fabric-api-bytes"),
	"lithium.jar":   []byte("lithium-bytes"),
	"jei.jar":       []byte("jei-bytes"),
}

func h1(b []byte) string   { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
func h512(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

type mock struct {
	srv  *httptest.Server
	mu   sync.Mutex // the handlers run concurrently (searches query both sites at once)
	hits map[string]int
}

func (m *mock) hit(key string) {
	m.mu.Lock()
	m.hits[key]++
	m.mu.Unlock()
}

func newMock(t *testing.T) (*mock, *Service) {
	m := &mock{hits: map[string]int{}}
	mux := http.NewServeMux()
	m.srv = httptest.NewServer(mux)
	base := m.srv.URL
	ver := func(id, pid, file, env string, deps []map[string]string) map[string]any {
		return map[string]any{
			"id": id, "project_id": pid, "version_number": "1.0", "version_type": "release",
			"date_published": "2025-01-01T00:00:00Z", "environment": env, "game_versions": []string{"1.21.1"}, "loaders": []string{"fabric"},
			"files":        []map[string]any{{"url": base + "/cdn/" + file, "filename": file, "primary": true, "size": len(jars[file]), "hashes": map[string]string{"sha1": h1(jars[file]), "sha512": h512(jars[file])}}},
			"dependencies": deps,
		}
	}
	versions := map[string]map[string]any{
		"SOD1": ver("SOD1", "AANobbMI", "sodium.jar", "client_only", []map[string]string{{"project_id": "P7dR8mSH", "dependency_type": "required"}}),
		"FAPI": ver("FAPI", "P7dR8mSH", "fabricapi.jar", "client_and_server", nil),
		"LITH": ver("LITH", "gvQqBUqZ", "lithium.jar", "", nil),
	}
	projects := map[string]map[string]any{
		"AANobbMI": {"id": "AANobbMI", "slug": "sodium", "title": "Sodium", "description": "Fast **renderer**", "body": "# Sodium\nBody", "client_side": "required", "server_side": "unsupported", "project_type": "mod", "team": "T1", "icon_url": "https://i/s.png"},
		"P7dR8mSH": {"id": "P7dR8mSH", "slug": "fabric-api", "title": "Fabric API", "description": "Core", "client_side": "required", "server_side": "required", "project_type": "mod", "team": "T2"},
		"gvQqBUqZ": {"id": "gvQqBUqZ", "slug": "lithium", "title": "Lithium", "description": "Server opt", "client_side": "optional", "server_side": "required", "project_type": "mod", "team": "T3"},
	}
	projectVersion := map[string]string{"AANobbMI": "SOD1", "P7dR8mSH": "FAPI", "gvQqBUqZ": "LITH", "sodium": "SOD1"}
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	idsParam := func(r *http.Request) []string {
		var ids []string
		json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids)
		return ids
	}
	mux.HandleFunc("/mr/", func(w http.ResponseWriter, r *http.Request) {
		m.hit(r.Method + " " + r.URL.Path)
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		p := strings.TrimPrefix(r.URL.Path, "/mr")
		switch {
		case p == "/search":
			facets := r.URL.Query().Get("facets")
			if !strings.Contains(facets, "versions:1.21.1") || !strings.Contains(facets, "categories:fabric") {
				t.Errorf("facets %s", facets)
			}
			js(w, map[string]any{"total_hits": 2, "hits": []map[string]any{
				{"project_id": "gvQqBUqZ", "slug": "lithium", "title": "Lithium", "description": "Server opt", "downloads": 10, "client_side": "optional", "server_side": "required", "author": "jelly"},
				{"project_id": "AANobbMI", "slug": "sodium", "title": "Sodium", "description": "Fast **renderer**", "downloads": 100, "client_side": "required", "server_side": "unsupported", "author": "jelly"},
			}})
		case p == "/projects":
			var out []any
			for _, id := range idsParam(r) {
				if pr, ok := projects[id]; ok {
					out = append(out, pr)
				}
			}
			js(w, out)
		case p == "/versions":
			var out []any
			for _, id := range idsParam(r) {
				if v, ok := versions[id]; ok {
					out = append(out, v)
				}
			}
			js(w, out)
		case p == "/teams":
			var out [][]map[string]any
			for _, id := range idsParam(r) {
				out = append(out, []map[string]any{{"team_id": id, "role": "Owner", "user": map[string]string{"username": "owner-" + id}}})
			}
			js(w, out)
		case p == "/version_files":
			var body struct{ Hashes []string }
			json.NewDecoder(r.Body).Decode(&body)
			out := map[string]any{}
			for _, h := range body.Hashes {
				for _, v := range versions {
					f := v["files"].([]map[string]any)[0]
					if f["hashes"].(map[string]string)["sha1"] == h {
						out[h] = v
					}
				}
			}
			js(w, out)
		case strings.HasPrefix(p, "/project/") && strings.HasSuffix(p, "/version"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/project/"), "/version")
			if !strings.Contains(r.URL.Query().Get("loaders"), "fabric") {
				t.Errorf("loaders not filtered: %s", r.URL.RawQuery)
			}
			if vid, ok := projectVersion[id]; ok {
				js(w, []any{versions[vid]})
			} else {
				js(w, []any{})
			}
		case strings.HasPrefix(p, "/project/") && strings.HasSuffix(p, "/members"):
			js(w, []map[string]any{{"role": "Owner", "user": map[string]string{"username": "jelly"}}})
		case strings.HasPrefix(p, "/project/"):
			id := strings.TrimPrefix(p, "/project/")
			if id == "sodium" {
				id = "AANobbMI"
			}
			if pr, ok := projects[id]; ok {
				js(w, pr)
			} else {
				http.NotFound(w, r)
			}
		case strings.HasPrefix(p, "/version/"):
			if v, ok := versions[strings.TrimPrefix(p, "/version/")]; ok {
				js(w, v)
			} else {
				http.NotFound(w, r)
			}
		default:
			t.Errorf("unexpected modrinth call %s", p)
			http.NotFound(w, r)
		}
	})
	jeiURL := base + "/cdn/jei.jar"
	cfFile := map[string]any{"id": 4593548, "modId": 238222, "displayName": "JEI", "fileName": "jei.jar", "releaseType": 1, "fileLength": len(jars["jei.jar"]),
		"downloadUrl": jeiURL, "gameVersions": []string{"1.21.1", "Client", "Server", "Fabric"}, "isAvailable": true,
		"hashes": []map[string]any{{"value": h1(jars["jei.jar"]), "algo": 1}}}
	cfMod := map[string]any{"id": 238222, "name": "Just Enough Items", "slug": "jei", "summary": "Recipes", "classId": 6, "authors": []map[string]string{{"name": "mezz"}}, "logo": map[string]string{"thumbnailUrl": "https://i/j.png"}}
	mux.HandleFunc("/cf/", func(w http.ResponseWriter, r *http.Request) {
		m.hit(r.Method + " " + r.URL.Path)
		if r.Header.Get("x-api-key") != "KEY" {
			w.WriteHeader(403)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/cf")
		switch p {
		case "/v1/mods/files":
			js(w, map[string]any{"data": []any{cfFile}})
		case "/v1/mods":
			js(w, map[string]any{"data": []any{cfMod}})
		case "/v1/mods/search":
			js(w, map[string]any{"data": []any{cfMod, map[string]any{"id": 9, "name": "Sodium", "slug": "sodium-cf", "summary": "dup", "downloadCount": 5}}, "pagination": map[string]int{"totalCount": 2}})
		case "/v1/mods/238222":
			js(w, map[string]any{"data": cfMod})
		case "/v1/mods/238222/files":
			js(w, map[string]any{"data": []any{cfFile}})
		case "/v1/mods/238222/description":
			js(w, map[string]any{"data": "<p>Hi <b>there</b></p>"})
		default:
			t.Errorf("unexpected curseforge call %s", p)
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, r *http.Request) {
		m.hit("cdn")
		b, ok := jars[strings.TrimPrefix(r.URL.Path, "/cdn/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})
	t.Cleanup(m.srv.Close)
	s := &Service{MR: &modrinth.Client{BaseURL: base + "/mr", HTTP: m.srv.Client()}, CF: &curseforge.Client{BaseURL: base + "/cf", APIKey: "KEY", HTTP: m.srv.Client()}}
	return m, s
}

func testPack() *model.Pack {
	p := model.New("T")
	p.Minecraft, p.Loader, p.LoaderVersion = "1.21.1", "fabric", "0.16.9"
	return p
}

func TestAddProjectWithDependencies(t *testing.T) {
	_, s := newMock(t)
	p := testPack()
	g := p.AddGroup("Perf")
	res, err := s.AddProject(context.Background(), p, model.SourceModrinth, "sodium", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 || len(p.Mods) != 2 {
		t.Fatalf("added %d mods", len(res.Added))
	}
	sod, api := p.Mods[0], p.Mods[1]
	if sod.Name != "Sodium" || sod.Side != model.SideClient || sod.Author != "jelly" || sod.GroupID != g.ID || sod.File.Path != "mods/sodium.jar" {
		t.Fatalf("sodium %+v", sod)
	}
	if api.Name != "Fabric API" || api.Side != model.SideBoth {
		t.Fatalf("api %+v", api)
	}
	// Adding again is a no-op.
	res, err = s.AddProject(context.Background(), p, model.SourceModrinth, "AANobbMI", "")
	if err != nil || len(res.Added) != 0 || len(res.Existing) != 1 {
		t.Fatalf("re-add: %v %+v", err, res)
	}
}

func TestNoCompatibleVersion(t *testing.T) {
	_, s := newMock(t)
	p := testPack()
	// project exists but mock returns no versions for unknown mapping
	delete(map[string]string{}, "")
	_, err := s.AddProject(context.Background(), p, model.SourceModrinth, "missing", "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAddCurseForge(t *testing.T) {
	_, s := newMock(t)
	p := testPack()
	res, err := s.AddProject(context.Background(), p, model.SourceCurseForge, "238222", "")
	if err != nil {
		t.Fatal(err)
	}
	m := res.Added[0]
	if m.Name != "Just Enough Items" || m.Author != "mezz" || m.Side != model.SideBoth || m.CurseForge.FileID != 4593548 || m.File.SHA1 == "" {
		t.Fatalf("%+v", m)
	}
}

func buildZip(t *testing.T, files map[string]string) string {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(b))
	}
	zw.Close()
	return buf.String()
}

func TestEnrichCurseForgePack(t *testing.T) {
	_, s := newMock(t)
	data := buildZip(t, map[string]string{"manifest.json": `{"minecraft":{"version":"1.21.1","modLoaders":[{"id":"fabric-0.16.9","primary":true}]},"manifestType":"minecraftModpack","name":"CF","version":"1","files":[{"projectID":238222,"fileID":4593548,"required":true}]}`})
	p, _, err := formats.Read([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Enrich(context.Background(), p, nil)
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings %v", rep.Warnings)
	}
	m := p.Mods[0]
	if m.Name != "Just Enough Items" || m.File.Path != "mods/jei.jar" || m.Side != model.SideBoth || len(m.File.URLs) == 0 {
		t.Fatalf("%+v", m)
	}
}

func TestEnrichWithoutKeyWarns(t *testing.T) {
	_, s := newMock(t)
	s.SetCurseForgeKey("")
	p := testPack()
	p.AddMod(&model.Mod{CurseForge: &model.CurseForgeRef{ProjectID: 1, FileID: 2}})
	rep := s.Enrich(context.Background(), p, nil)
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "API key") {
		t.Fatalf("%v", rep.Warnings)
	}
}

func TestEnrichMrpackByHashAndProjects(t *testing.T) {
	_, s := newMock(t)
	p := testPack()
	// Lithium only known by hash (e.g. a jar from a GitHub URL).
	p.AddMod(&model.Mod{File: model.File{Path: "mods/lithium.jar", SHA1: h1(jars["lithium.jar"]), URLs: []string{"https://github.com/x/lithium.jar"}}, Side: model.SideUnknown})
	p.AddMod(&model.Mod{File: model.File{Path: "mods/sodium.jar"}, Modrinth: &model.ModrinthRef{ProjectID: "AANobbMI", VersionID: "SOD1"}, Side: model.SideUnknown})
	s.Enrich(context.Background(), p, nil)
	l, sd := p.Mods[0], p.Mods[1]
	if l.Modrinth == nil || l.Name != "Lithium" || l.Side != model.SideServer || l.Author != "owner-T3" || !strings.Contains(l.File.URLs[0], "/cdn/") {
		t.Fatalf("lithium %+v %+v", l, l.Modrinth)
	}
	if sd.Name != "Sodium" || sd.Side != model.SideClient {
		t.Fatalf("sodium %+v", sd)
	}
}

func TestSearchMergesSources(t *testing.T) {
	_, s := newMock(t)
	res := s.Search(context.Background(), SearchRequest{Query: "sodium", Sources: []model.Source{model.SourceModrinth, model.SourceCurseForge}, Minecraft: "1.21.1", Loader: "fabric"})
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	if len(res.Hits) != 3 {
		for _, h := range res.Hits {
			t.Log(h.Source, h.Title)
		}
		t.Fatalf("expected 3 merged hits, got %d", len(res.Hits))
	}
	if res.Hits[0].Title != "Sodium" || res.Hits[0].Source != model.SourceModrinth || res.Hits[0].AlsoOn != model.SourceCurseForge {
		t.Fatalf("top hit %+v", res.Hits[0])
	}
	// Missing key on CurseForge surfaces as a per-source error, not a failure.
	s.SetCurseForgeKey("")
	res = s.Search(context.Background(), SearchRequest{Query: "x", Sources: []model.Source{model.SourceModrinth, model.SourceCurseForge}, Minecraft: "1.21.1", Loader: "fabric"})
	if res.Errors[model.SourceCurseForge] == nil || len(res.Hits) != 2 {
		t.Fatalf("errors %v hits %d", res.Errors, len(res.Hits))
	}
}

func TestDetails(t *testing.T) {
	_, s := newMock(t)
	d, err := s.ProjectDetails(context.Background(), &Hit{Source: model.SourceCurseForge, ID: "238222", Slug: "jei"})
	if err != nil || !d.BodyIsHTML || !strings.Contains(d.Body, "there") {
		t.Fatal(err, d)
	}
	d, err = s.ProjectDetails(context.Background(), &Hit{Source: model.SourceModrinth, ID: "AANobbMI"})
	if err != nil || d.BodyIsHTML || !strings.Contains(d.Body, "# Sodium") || len(d.Links) == 0 {
		t.Fatal(err, d)
	}
}

func readZip(t *testing.T, data []byte) map[string][]byte {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func fullPack(t *testing.T, s *Service) *model.Pack {
	p := testPack()
	if _, err := s.AddProject(context.Background(), p, model.SourceModrinth, "sodium", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProject(context.Background(), p, model.SourceModrinth, "gvQqBUqZ", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProject(context.Background(), p, model.SourceCurseForge, "238222", ""); err != nil {
		t.Fatal(err)
	}
	p.Overrides = []*model.OverrideFile{
		{Side: model.SideBoth, Path: "config/common.toml", Data: []byte("c")},
		{Side: model.SideClient, Path: "options.txt", Data: []byte("o")},
		{Side: model.SideServer, Path: "server.properties", Data: []byte("s")},
	}
	return p
}

func TestExports(t *testing.T) {
	_, s := newMock(t)
	p := fullPack(t, s)
	dl := &Downloader{HTTP: http.DefaultClient, CacheDir: t.TempDir()}
	// Treat the mock's Modrinth files (/cdn/<jar> except jei) as Modrinth-hosted.
	old := ModrinthAllowedPrefixes
	ModrinthAllowedPrefixes = append([]string{s.MR.BaseURL[:len(s.MR.BaseURL)-3] + "/cdn/sodium", s.MR.BaseURL[:len(s.MR.BaseURL)-3] + "/cdn/fabricapi", s.MR.BaseURL[:len(s.MR.BaseURL)-3] + "/cdn/lithium"}, old...)
	defer func() { ModrinthAllowedPrefixes = old }()
	ctx := context.Background()

	// Client-only: just Sodium + client overrides.
	data, res, err := s.Export(ctx, p, ExportOptions{Target: TargetClient, Format: FormatMrpack}, dl, nil)
	if err != nil {
		t.Fatal(err)
	}
	cp, _, err := formats.Read(data)
	if err != nil || len(cp.Mods) != 1 || cp.Mods[0].File.Path != "mods/sodium.jar" || res.Mods != 1 {
		t.Fatalf("client export: %v %+v", err, cp)
	}
	if len(cp.Overrides) != 1 || cp.Overrides[0].Path != "options.txt" {
		t.Fatalf("client overrides %+v", cp.Overrides)
	}

	// Common: Fabric API + JEI (non-Modrinth URL -> warning, still referenced).
	data, res, err = s.Export(ctx, p, ExportOptions{Target: TargetCommon, Format: FormatMrpack}, dl, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm, _, _ := formats.Read(data)
	names := map[string]bool{}
	for _, m := range cm.Mods {
		names[m.File.Path] = true
	}
	if len(cm.Mods) != 2 || !names["mods/fabricapi.jar"] || !names["mods/jei.jar"] || len(cm.Overrides) != 1 {
		t.Fatalf("common export %v overrides %d", names, len(cm.Overrides))
	}

	// Common with embedding: JEI goes into overrides as a jar.
	data, res, err = s.Export(ctx, p, ExportOptions{Target: TargetCommon, Format: FormatMrpack, EmbedNonModrinth: true}, dl, nil)
	if err != nil || res.Embedded != 1 {
		t.Fatalf("embed: %v %+v", err, res)
	}
	files := readZip(t, data)
	if string(files["overrides/mods/jei.jar"]) != "jei-bytes" {
		t.Fatalf("embedded jar missing: %v", keys(files))
	}

	// Server, ready-to-run zip including common mods.
	data, res, err = s.Export(ctx, p, DefaultExportOptions(TargetServer), dl, nil)
	if err != nil {
		t.Fatal(err)
	}
	files = readZip(t, data)
	for _, want := range []string{"mods/lithium.jar", "mods/fabricapi.jar", "mods/jei.jar", "config/common.toml", "server.properties"} {
		if _, ok := files[want]; !ok {
			t.Errorf("server zip missing %s (has %v)", want, keys(files))
		}
	}
	if _, ok := files["mods/sodium.jar"]; ok {
		t.Error("client mod in server zip")
	}
	if _, ok := files["options.txt"]; ok {
		t.Error("client override in server zip")
	}
	if string(files["mods/lithium.jar"]) != "lithium-bytes" || len(res.Skipped) != 0 {
		t.Fatalf("bad jar / skipped %v", res.Skipped)
	}

	// Server-only without common mods: just Lithium, as CurseForge zip (embedded since not on CF).
	data, _, err = s.Export(ctx, p, ExportOptions{Target: TargetServer, Format: FormatCurseForge}, dl, nil)
	if err != nil {
		t.Fatal(err)
	}
	files = readZip(t, data)
	if _, ok := files["overrides/mods/lithium.jar"]; !ok || !strings.Contains(string(files["manifest.json"]), `"files": []`) {
		t.Fatalf("cf server export %v %s", keys(files), files["manifest.json"])
	}
}

func TestExportVerifiesHashes(t *testing.T) {
	_, s := newMock(t)
	p := testPack()
	p.AddMod(&model.Mod{Name: "Bad", Side: model.SideServer, File: model.File{Path: "mods/x.jar", SHA1: "deadbeef", URLs: []string{s.MR.BaseURL[:len(s.MR.BaseURL)-3] + "/cdn/sodium.jar"}}})
	_, res, err := s.Export(context.Background(), p, ExportOptions{Target: TargetServer, Format: FormatZip}, &Downloader{CacheDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "sha1 mismatch") || res.Mods != 0 {
		t.Fatalf("%+v", res)
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

func TestFilter(t *testing.T) {
	p := testPack()
	g := p.AddGroup("World gen")
	a := &model.Mod{Name: "Terralith", Summary: "Adds **biomes**", Side: model.SideBoth, GroupID: g.ID, Modrinth: &model.ModrinthRef{ProjectID: "x"}}
	b := &model.Mod{Name: "Sodium", Side: model.SideClient, CurseForge: &model.CurseForgeRef{ProjectID: 1}}
	p.AddMod(a)
	p.AddMod(b)
	cases := map[string][]*model.Mod{
		"":                  {a, b},
		"biomes":            {a}, // matches inside stripped markdown
		"**":                {},  // formatting characters never match
		"side:client":       {b},
		"source:cf":         {b},
		`group:"world gen"`: {a},
		"sod side:both":     {},
		"type:mod terra":    {a},
	}
	for q, want := range cases {
		f := ParseFilter(q)
		var got []*model.Mod
		for _, m := range p.Mods {
			if f.Match(p, m) {
				got = append(got, m)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%q: got %d want %d", q, len(got), len(want))
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: mismatch", q)
			}
		}
	}
}

func TestNeoForgePrefix(t *testing.T) {
	for in, want := range map[string]string{"1.21.1": "21.1.", "1.21": "21.0.", "1.20.4": "20.4.", "26.1": "26.1.", "": "", "24w10a": ""} {
		if got := neoForgePrefix(in); got != want {
			t.Errorf("%s -> %q want %q", in, got, want)
		}
	}
}

func TestSuggestedFileName(t *testing.T) {
	p := testPack()
	p.Name, p.Version = "My: Pack", "1.0"
	if got := SuggestedFileName(p, ExportOptions{Target: TargetServer, Format: FormatZip}); got != "My_-Pack-1.0-server.zip" {
		t.Fatal(got)
	}
}

func TestDocumentNoticesChangesOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.mpm")
	d := NewDocument("Watched", "1.21.1", "fabric", "0.16.9")
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	if d.ChangedOnDisk() {
		t.Fatal("our own save counts as a change")
	}
	// Another program saves the same file.
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other.Pack.Name = "Changed elsewhere"
	time.Sleep(20 * time.Millisecond) // make sure the modification time moves
	if err := other.Save(""); err != nil {
		t.Fatal(err)
	}
	if !d.ChangedOnDisk() {
		t.Fatal("change not noticed")
	}
	d.Pack.Name = "unsaved edit"
	d.Dirty = true
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
	if d.Pack.Name != "Changed elsewhere" || d.Dirty || d.ChangedOnDisk() {
		t.Fatalf("after reload: %q dirty=%v changed=%v", d.Pack.Name, d.Dirty, d.ChangedOnDisk())
	}
	// A replacement with the same size and modification time (coarse file
	// system clocks) is still noticed.
	st, _ := os.Stat(path)
	same, _ := Open(path)
	same.Pack.Name = "Changed elsewherE" // same length
	if err := same.Save(""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(path); st2.Size() != st.Size() || !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("test setup: size or time differs")
	}
	if !d.ChangedOnDisk() {
		t.Fatal("same-size replacement not noticed")
	}
	// Acknowledging hides this change, but not the next one.
	d.AcknowledgeDiskChange()
	if d.ChangedOnDisk() {
		t.Fatal("acknowledged change still reported")
	}
	time.Sleep(20 * time.Millisecond)
	if err := other.Save(""); err != nil {
		t.Fatal(err)
	}
	if !d.ChangedOnDisk() {
		t.Fatal("change after acknowledging not noticed")
	}
	// A deleted file isn't a change to reload.
	os.Remove(path)
	if d.ChangedOnDisk() {
		t.Fatal("deleted file reported as changed")
	}
	if NewDocument("x", "", "fabric", "").ChangedOnDisk() {
		t.Fatal("unsaved document reported as changed")
	}
}
