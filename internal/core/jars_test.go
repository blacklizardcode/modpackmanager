package core

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/api/modrinth"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/jarmeta"
	"modpackmanager/internal/model"
)

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fabricJarFor(t *testing.T, id, version, env string) []byte {
	return zipOf(t, map[string][]byte{"fabric.mod.json": []byte(`{"schemaVersion":1,"id":"` + id + `","name":"` + strings.ToUpper(id[:1]) + id[1:] + `","version":"` + version + `","environment":"` + env + `","description":"About ` + id + `"}`)})
}

// mrMock answers the Modrinth calls PrepareJars makes. Jars in onModrinth are
// "published" there; down makes every call fail.
type mrMock struct {
	onModrinth map[string][]byte // version id -> jar
	down       bool
	calls      []string
}

func (mm *mrMock) service(t *testing.T) *Service {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mm.calls = append(mm.calls, r.Method+" "+r.URL.Path)
		if mm.down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		version := func(id string, jar []byte) map[string]any {
			return map[string]any{"id": id, "project_id": "P-" + id, "version_number": "9.9", "environment": "client_only",
				"files": []map[string]any{{"url": "https://cdn.modrinth.com/data/P-" + id + "/versions/" + id + "/f.jar", "filename": "f.jar", "primary": true,
					"size": len(jar), "hashes": map[string]string{"sha1": h1(jar), "sha512": h512(jar)}}}}
		}
		ids := func() []string {
			var out []string
			json.Unmarshal([]byte(r.URL.Query().Get("ids")), &out)
			return out
		}
		switch r.URL.Path {
		case "/version_files":
			var body struct{ Hashes []string }
			json.NewDecoder(r.Body).Decode(&body)
			out := map[string]any{}
			for _, h := range body.Hashes {
				for id, jar := range mm.onModrinth {
					if h1(jar) == h {
						out[h] = version(id, jar)
					}
				}
			}
			json.NewEncoder(w).Encode(out)
		case "/projects":
			var out []any
			for _, id := range ids() {
				out = append(out, map[string]any{"id": id, "slug": strings.ToLower(id), "title": "Published " + id, "description": "From Modrinth",
					"project_type": "mod", "team": "T", "icon_url": "https://cdn.modrinth.com/icon.png", "client_side": "required", "server_side": "unsupported"})
			}
			json.NewEncoder(w).Encode(out)
		case "/teams":
			json.NewEncoder(w).Encode([][]map[string]any{{{"team_id": "T", "role": "Owner", "user": map[string]string{"username": "dev"}}}})
		case "/versions":
			var out []any
			for _, id := range ids() {
				if jar, ok := mm.onModrinth[id]; ok {
					out = append(out, version(id, jar))
				}
			}
			json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected Modrinth call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Service{MR: &modrinth.Client{BaseURL: srv.URL, HTTP: srv.Client()}, CF: &curseforge.Client{}}
}

func fabricPack() *model.Pack {
	p := model.New("Mine")
	p.Loader = "fabric"
	return p
}

func TestAddJarsStoresLocalJars(t *testing.T) {
	mm := &mrMock{}
	s := mm.service(t)
	p := fabricPack()
	g := p.AddGroup("Mine")
	jar := fabricJarFor(t, "zoomy", "1.0", "client")
	res, rep := s.AddJars(context.Background(), p, []JarFile{{Name: "zoomy-1.0.jar", Data: jar}}, JarOptions{GroupID: g.ID})
	if len(res) != 1 || res[0].Err != nil || res[0].Linked || res[0].Existing || len(res[0].Warnings) != 0 || len(rep.Warnings) != 0 {
		t.Fatalf("%+v %+v", res, rep)
	}
	m := res[0].Mod
	if len(p.Mods) != 1 || p.Mods[0] != m || m.Name != "Zoomy" || m.Summary != "About zoomy" || m.Side != model.SideClient ||
		m.SideLocked || m.GroupID != g.ID || !bytes.Equal(m.File.Data, jar) || m.File.Path != "mods/zoomy-1.0.jar" ||
		m.Local.ModID != "zoomy" || m.Local.Version != "1.0" || m.SourceLabel() != "Local file" {
		t.Fatalf("%+v %+v", m, m.Local)
	}
	if len(mm.calls) != 1 || mm.calls[0] != "POST /version_files" {
		t.Fatalf("calls %v", mm.calls)
	}
	if title, problems := JarSummary(res, rep); title != "Added Zoomy" || len(problems) != 0 {
		t.Fatalf("%q %q", title, problems)
	}
	if !JarsChanged(res) {
		t.Fatal("JarsChanged")
	}
}

func TestAddJarsLinksFilesOnModrinth(t *testing.T) {
	jar := fabricJarFor(t, "pub", "9.9", "*")
	mm := &mrMock{onModrinth: map[string][]byte{"V1": jar}}
	s := mm.service(t)
	p := fabricPack()
	res, rep := s.AddJars(context.Background(), p, []JarFile{{Name: "pub.jar", Data: jar}}, JarOptions{})
	m := res[0].Mod
	if res[0].Err != nil || !res[0].Linked || m.File.Stored() || m.Modrinth == nil || m.Modrinth.ProjectID != "P-V1" ||
		m.Name != "Published P-V1" || m.Author != "dev" || m.Side != model.SideClient || len(m.File.URLs) != 1 ||
		!strings.HasPrefix(m.File.URLs[0], "https://cdn.modrinth.com/") || m.SourceLabel() != "Modrinth" || len(rep.Warnings) != 0 {
		t.Fatalf("%+v %+v %+v", res[0], m, rep)
	}
	if title, _ := JarSummary(res, rep); title != "Added Published P-V1 (found on Modrinth, so the pack links to it)" {
		t.Fatal(title)
	}
	// Adding it again is a no-op.
	res, _ = s.AddJars(context.Background(), p, []JarFile{{Name: "pub-copy.jar", Data: jar}}, JarOptions{})
	if !res[0].Existing || res[0].Mod != m || len(p.Mods) != 1 || JarsChanged(res) {
		t.Fatalf("%+v", res[0])
	}
}

func TestAddJarsReplacesOlderBuilds(t *testing.T) {
	s := (&mrMock{}).service(t)
	p := fabricPack()
	g := p.AddGroup("Own")
	old := fabricJarFor(t, "mine", "1.0", "*")
	res, _ := s.AddJars(context.Background(), p, []JarFile{{Name: "mine-1.0.jar", Data: old}}, JarOptions{GroupID: g.ID, Side: model.SideServer, Offline: true})
	first := res[0].Mod
	first.Notes = "keep me"
	if !first.SideLocked || first.Side != model.SideServer {
		t.Fatalf("side option not applied: %+v", first)
	}
	// Same mod id, new file name: an update.
	res, _ = s.AddJars(context.Background(), p, []JarFile{{Name: "mine-1.1.jar", Data: fabricJarFor(t, "mine", "1.1", "*")}}, JarOptions{Offline: true})
	m := res[0].Mod
	if res[0].Replaced != first || len(p.Mods) != 1 || m.GroupID != g.ID || m.Notes != "keep me" || m.Side != model.SideServer || !m.SideLocked || m.Local.Version != "1.1" {
		t.Fatalf("%+v %+v", res[0], m)
	}
	if title, problems := JarSummary(res, Report{}); title != "Updated Mine (mine-1.1.jar replaces mine-1.0.jar)" || len(problems) != 0 {
		t.Fatalf("%q %q", title, problems)
	}
	// Same file name, different contents (a rebuilt jar without metadata).
	res, _ = s.AddJars(context.Background(), p, []JarFile{{Name: "mine-1.1.jar", Data: zipOf(t, map[string][]byte{"x.class": []byte("x")})}}, JarOptions{Offline: true})
	if res[0].Replaced != m || len(p.Mods) != 1 {
		t.Fatalf("%+v", res[0])
	}
	// A linked mod with the same file name is a conflict, not an update.
	p.AddMod(&model.Mod{Name: "Sodium", File: model.File{Path: "mods/sodium.jar", URLs: []string{"https://cdn.modrinth.com/x"}}, Modrinth: &model.ModrinthRef{ProjectID: "AA"}})
	res, _ = s.AddJars(context.Background(), p, []JarFile{{Name: "sodium.jar", Data: fabricJarFor(t, "sodiumfork", "1", "client")}}, JarOptions{Offline: true})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "same file name as Sodium (from Modrinth)") || len(p.Mods) != 2 {
		t.Fatalf("%+v", res[0])
	}
}

func TestAddJarsProblems(t *testing.T) {
	mm := &mrMock{down: true}
	s := mm.service(t)
	p := fabricPack()
	forge := zipOf(t, map[string][]byte{"META-INF/mods.toml": []byte("[[mods]]\nmodId=\"fm\"\ndisplayName=\"Forge Mod\"\n")})
	plain := zipOf(t, map[string][]byte{"lib/A.class": []byte("x")})
	res, rep := s.AddJars(context.Background(), p, []JarFile{
		{Name: "forge.jar", Data: forge},
		{Name: "plain.jar", Data: plain},
		{Name: "garbage.jar", Data: []byte("nope")},
		{Name: "notes.txt", Data: []byte("x")},
		{Name: "empty.jar", Data: []byte{}},
		{Name: "gone.jar", Err: errors.New("couldn't be read: no such file or directory")},
	}, JarOptions{})
	if len(p.Mods) != 2 || res[0].Mod == nil || res[1].Mod == nil {
		t.Fatalf("%d mods", len(p.Mods))
	}
	if len(res[0].Warnings) != 1 || res[0].Warnings[0] != "it's a Forge mod, but this pack uses Fabric" {
		t.Errorf("forge warning %q", res[0].Warnings)
	}
	if len(res[1].Warnings) != 1 || !strings.Contains(res[1].Warnings[0], "no mod metadata") || res[1].Mod.Side != model.SideUnknown {
		t.Errorf("plain %+v", res[1])
	}
	if !errors.Is(res[2].Err, jarmeta.ErrNotJar) || res[3].Err == nil || res[4].Err == nil || res[5].Err == nil {
		t.Errorf("errors %v / %v / %v / %v", res[2].Err, res[3].Err, res[4].Err, res[5].Err)
	}
	// Modrinth being down is reported once and doesn't stop anything.
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "Modrinth hash lookup failed") {
		t.Errorf("report %q", rep.Warnings)
	}
	title, problems := JarSummary(res, rep)
	if title != "Added 2 JAR files" || len(problems) != 7 ||
		problems[2] != "garbage.jar isn't a valid .jar file (not a zip archive)." || problems[3] != "notes.txt isn't a .jar file." ||
		problems[5] != "gone.jar couldn't be read: no such file or directory." {
		t.Fatalf("%q\n%q", title, strings.Join(problems, "\n"))
	}
	if title, problems := JarSummary(res[2:3], Report{}); title != "Couldn't add garbage.jar" || problems[0] != "It isn't a valid .jar file (not a zip archive)." {
		t.Fatalf("%q %q", title, problems)
	}
}

func TestReadJarFiles(t *testing.T) {
	dir := t.TempDir()
	mods := filepath.Join(dir, "mods")
	os.Mkdir(mods, 0o755)
	os.Mkdir(filepath.Join(dir, "empty"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.jar"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("B"), 0o644)
	os.WriteFile(filepath.Join(mods, "c.JAR"), []byte("C"), 0o644)
	os.WriteFile(filepath.Join(mods, "d.jar"), []byte("D"), 0o644)
	os.WriteFile(filepath.Join(mods, "readme.md"), []byte("R"), 0o644)
	files := ReadJarFiles([]string{filepath.Join(dir, "a.jar"), filepath.Join(dir, "b.txt"), mods, filepath.Join(dir, "empty"), filepath.Join(dir, "missing.jar")})
	var got []string
	for _, f := range files {
		s := f.Name + "=" + string(f.Data)
		if f.Err != nil {
			s = f.Name + " " + f.Err.Error()
		}
		got = append(got, s)
	}
	want := "a.jar=A|b.txt isn't a .jar file|c.JAR=C|d.jar=D|empty is a folder with no .jar files in it|missing.jar couldn't be read: no such file or directory"
	if strings.Join(got, "|") != want {
		t.Fatalf("\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
}

func TestEnrichLinksStoredJars(t *testing.T) {
	jar := fabricJarFor(t, "pub", "9.9", "*")
	s := (&mrMock{onModrinth: map[string][]byte{"V1": jar}}).service(t)
	p := fabricPack()
	p.AddMod(formats.LocalMod("mods/pub.jar", jar, nil))
	s.Enrich(context.Background(), p, nil)
	if m := p.Mods[0]; m.Modrinth == nil || m.File.Stored() || m.Name != "Published P-V1" {
		t.Fatalf("%+v", m)
	}
}

func TestExportBundlesStoredJars(t *testing.T) {
	s := (&mrMock{}).service(t)
	p := fabricPack()
	client := fabricJarFor(t, "cl", "1", "client")
	server := fabricJarFor(t, "sv", "1", "server")
	both := fabricJarFor(t, "bo", "1", "*")
	for name, data := range map[string][]byte{"cl.jar": client, "sv.jar": server, "bo.jar": both} {
		if res, _ := s.AddJars(context.Background(), p, []JarFile{{Name: name, Data: data}}, JarOptions{Offline: true}); res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
	}
	// No network and no cache: everything must come from the pack itself.
	dl := &Downloader{HTTP: &http.Client{Transport: failTransport{}}, CacheDir: filepath.Join(t.TempDir(), "none")}
	export := func(opt ExportOptions) map[string][]byte {
		data, res, err := s.Export(context.Background(), p, opt, dl, nil)
		if err != nil || len(res.Skipped) != 0 {
			t.Fatalf("%v: %v %v", opt, err, res.Skipped)
		}
		out := map[string][]byte{}
		zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		for _, f := range zr.File {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			out[f.Name] = b.Bytes()
		}
		return out
	}
	full := export(ExportOptions{Target: TargetFull, Format: FormatMrpack})
	if !bytes.Equal(full["client-overrides/mods/cl.jar"], client) || !bytes.Equal(full["server-overrides/mods/sv.jar"], server) ||
		!bytes.Equal(full["overrides/mods/bo.jar"], both) || strings.Contains(string(full["modrinth.index.json"]), ".jar") {
		t.Fatalf("mrpack entries %v\n%s", keys(full), full["modrinth.index.json"])
	}
	cf := export(ExportOptions{Target: TargetClient, Format: FormatCurseForge, IncludeCommon: true})
	if !bytes.Equal(cf["overrides/mods/cl.jar"], client) || !bytes.Equal(cf["overrides/mods/bo.jar"], both) || cf["overrides/mods/sv.jar"] != nil {
		t.Fatalf("curseforge entries %v", keys(cf))
	}
	srv := export(ExportOptions{Target: TargetServer, Format: FormatZip, IncludeCommon: true})
	if !bytes.Equal(srv["mods/sv.jar"], server) || !bytes.Equal(srv["mods/bo.jar"], both) || srv["mods/cl.jar"] != nil {
		t.Fatalf("server zip entries %v", keys(srv))
	}
	if n := CountStored(SelectMods(p, TargetServer, true)); n != 2 {
		t.Fatalf("CountStored %d", n)
	}
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in this test")
}

func TestJarIcon(t *testing.T) {
	square := pngOf(t, 128, 128)
	banner := pngOf(t, 400, 100)
	if got := JarIcon(zipOf(t, map[string][]byte{"fabric.mod.json": []byte(`{"id":"x","icon":"i.png"}`), "i.png": square}), 64); got == nil {
		t.Fatal("square icon not returned")
	} else if cfg, _, err := image.DecodeConfig(bytes.NewReader(got)); err != nil || cfg.Width != 64 || cfg.Height != 64 {
		t.Fatalf("icon %v %v", cfg, err)
	}
	if JarIcon(zipOf(t, map[string][]byte{"META-INF/mods.toml": []byte("[[mods]]\nlogoFile=\"logo.png\"\n"), "logo.png": banner}), 64) != nil {
		t.Fatal("banner logo used as an icon")
	}
	if JarIcon(zipOf(t, map[string][]byte{"fabric.mod.json": []byte(`{"id":"x","icon":"i.png"}`), "i.png": []byte("not an image")}), 64) != nil {
		t.Fatal("garbage icon returned")
	}
	if JarIcon([]byte("not a jar"), 64) != nil {
		t.Fatal("icon from garbage")
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 50, 50, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
