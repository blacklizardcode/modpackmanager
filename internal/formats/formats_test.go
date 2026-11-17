package formats

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modpackmanager/internal/model"
)

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const sampleIndex = `{
  "formatVersion": 1, "game": "minecraft", "versionId": "2.0", "name": "Test Pack", "summary": "hi",
  "files": [
    {"path": "mods/sodium.jar", "hashes": {"sha1": "aa", "sha512": "bb"}, "env": {"client": "required", "server": "unsupported"},
     "downloads": ["https://cdn.modrinth.com/data/AANobbMI/versions/OihdIimA/sodium.jar"], "fileSize": 10},
    {"path": "mods/lithium.jar", "hashes": {"sha1": "cc", "sha512": "dd"}, "env": {"client": "optional", "server": "required"},
     "downloads": ["https://cdn.modrinth.com/data/gvQqBUqZ/versions/abc/lithium.jar"], "fileSize": 11},
    {"path": "mods/jei.jar", "hashes": {"sha1": "ee", "sha512": "ff"},
     "downloads": ["https://edge.forgecdn.net/files/4593/548/jei.jar"], "fileSize": 12},
    {"path": "resourcepacks/faithful.zip", "hashes": {"sha1": "11", "sha512": "22"}, "env": {"client": "required", "server": "required"},
     "downloads": ["https://cdn.modrinth.com/data/P1/versions/V1/faithful.zip"], "fileSize": 13}
  ],
  "dependencies": {"minecraft": "1.20.1", "fabric-loader": "0.15.11"}
}`

func TestReadMrpack(t *testing.T) {
	data := buildZip(t, map[string]string{
		"modrinth.index.json":           sampleIndex,
		"overrides/config/a.toml":       "a=1",
		"client-overrides/options.txt":  "fov:90",
		"server-overrides/server.props": "motd=x",
	})
	p, kind, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindMrpack {
		t.Fatalf("kind %v", kind)
	}
	if p.Name != "Test Pack" || p.Version != "2.0" || p.Minecraft != "1.20.1" || p.Loader != "fabric" || p.LoaderVersion != "0.15.11" {
		t.Fatalf("bad meta %+v", p)
	}
	if len(p.Mods) != 4 {
		t.Fatalf("mods %d", len(p.Mods))
	}
	byPath := map[string]*model.Mod{}
	for _, m := range p.Mods {
		byPath[m.File.Path] = m
	}
	s := byPath["mods/sodium.jar"]
	if s.Side != model.SideClient || s.Modrinth == nil || s.Modrinth.ProjectID != "AANobbMI" || s.Modrinth.VersionID != "OihdIimA" {
		t.Fatalf("sodium %+v %+v", s, s.Modrinth)
	}
	if byPath["mods/lithium.jar"].Side != model.SideServer {
		t.Fatalf("lithium side %v", byPath["mods/lithium.jar"].Side)
	}
	j := byPath["mods/jei.jar"]
	if j.Side != model.SideUnknown || j.CurseForge == nil || j.CurseForge.FileID != 4593548 {
		t.Fatalf("jei %+v %+v", j, j.CurseForge)
	}
	if byPath["resourcepacks/faithful.zip"].ProjectType != "resourcepack" {
		t.Fatal("resourcepack type")
	}
	if len(p.Overrides) != 3 {
		t.Fatalf("overrides %d", len(p.Overrides))
	}
	sides := map[string]model.Side{}
	for _, o := range p.Overrides {
		sides[o.Path] = o.Side
	}
	if sides["config/a.toml"] != model.SideBoth || sides["options.txt"] != model.SideClient || sides["server.props"] != model.SideServer {
		t.Fatalf("override sides %v", sides)
	}
}

func TestMrpackRejectsTraversal(t *testing.T) {
	bad := strings.Replace(sampleIndex, "mods/sodium.jar", "../../.bashrc", 1)
	if _, _, err := Read(buildZip(t, map[string]string{"modrinth.index.json": bad})); err == nil {
		t.Fatal("expected traversal error")
	}
	if _, _, err := Read(buildZip(t, map[string]string{"modrinth.index.json": sampleIndex, "overrides/../evil": "x"})); err == nil {
		t.Fatal("expected override traversal error")
	}
	for _, p := range []string{"/etc/passwd", "C:/x", "a/../../b", "..\\x"} {
		if SafeRelPath(p) == nil {
			t.Errorf("SafeRelPath(%q) should fail", p)
		}
	}
	for _, p := range []string{"mods/a.jar", "config/x/../y.toml"} {
		if err := SafeRelPath(p); err != nil {
			t.Errorf("SafeRelPath(%q) = %v", p, err)
		}
	}
}

func TestMrpackRoundTrip(t *testing.T) {
	p, _, err := Read(buildZip(t, map[string]string{"modrinth.index.json": sampleIndex, "client-overrides/o.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteMrpack(&buf, p, p.Mods, p.Overrides, MrpackWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	p2, kind, err := Read(buf.Bytes())
	if err != nil || kind != KindMrpack {
		t.Fatal(err, kind)
	}
	if len(p2.Mods) != 4 || p2.LoaderVersion != "0.15.11" || len(p2.Overrides) != 1 || p2.Overrides[0].Side != model.SideClient {
		t.Fatalf("round trip lost data: %+v", p2)
	}
	// JEI had no env: it must be exported as required/required (common).
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	var idx MrpackIndex
	readJSON(findFile(zr, "modrinth.index.json"), &idx)
	for _, f := range idx.Files {
		if f.Path == "mods/jei.jar" && (f.Env.Client != "required" || f.Env.Server != "required") {
			t.Fatalf("jei env %+v", f.Env)
		}
		if f.Path == "mods/sodium.jar" && f.Env.Server != "unsupported" {
			t.Fatalf("sodium env %+v", f.Env)
		}
	}
}

func TestMrpackLockedSideWins(t *testing.T) {
	p, _, _ := Read(buildZip(t, map[string]string{"modrinth.index.json": sampleIndex}))
	for _, m := range p.Mods {
		if m.File.Path == "mods/sodium.jar" {
			m.Side, m.SideLocked = model.SideBoth, true
		}
	}
	var buf bytes.Buffer
	if err := WriteMrpack(&buf, p, p.Mods, nil, MrpackWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	p2, _, _ := Read(buf.Bytes())
	for _, m := range p2.Mods {
		if m.File.Path == "mods/sodium.jar" && m.Side != model.SideBoth {
			t.Fatalf("locked side not exported: %v", m.Side)
		}
	}
}

const sampleManifest = `{
  "minecraft": {"version": "1.20.1", "modLoaders": [{"id": "forge-47.2.0", "primary": true}]},
  "manifestType": "minecraftModpack", "manifestVersion": 1,
  "name": "CF Pack", "version": "1.2", "author": "me",
  "files": [{"projectID": 238222, "fileID": 4593548, "required": true}, {"projectID": 306612, "fileID": 4712345, "required": true}],
  "overrides": "overrides"
}`

func TestReadCurseForge(t *testing.T) {
	p, kind, err := Read(buildZip(t, map[string]string{
		"manifest.json":         sampleManifest,
		"modlist.html":          "<ul></ul>",
		"overrides/config/x.js": "y",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindCurseForge || p.Loader != "forge" || p.LoaderVersion != "47.2.0" || p.Minecraft != "1.20.1" || p.Author != "me" {
		t.Fatalf("meta %+v", p)
	}
	if len(p.Mods) != 2 || p.Mods[0].CurseForge.ProjectID != 238222 || p.Mods[0].CurseForge.FileID != 4593548 {
		t.Fatalf("mods %+v", p.Mods)
	}
	if len(p.Overrides) != 1 || p.Overrides[0].Path != "config/x.js" {
		t.Fatalf("overrides %+v", p.Overrides)
	}
	// Writing back produces an equivalent manifest.
	var buf bytes.Buffer
	if err := WriteCurseForge(&buf, p, p.Mods, p.Overrides, CurseForgeWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	p2, _, err := Read(buf.Bytes())
	if err != nil || len(p2.Mods) != 2 || p2.LoaderVersion != "47.2.0" || len(p2.Overrides) != 1 {
		t.Fatalf("round trip: %v %+v", err, p2)
	}
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if findFile(zr, "modlist.html") == nil {
		t.Fatal("modlist.html missing")
	}
}

func TestParseCFLoader(t *testing.T) {
	cases := map[string][2]string{
		"forge-47.2.0":    {"forge", "47.2.0"},
		"neoforge-21.1.7": {"neoforge", "21.1.7"},
		"fabric-0.16.9":   {"fabric", "0.16.9"},
		"quilt-0.26.0":    {"quilt", "0.26.0"},
		"weird":           {"", ""},
	}
	for in, want := range cases {
		l, v := ParseCFLoader(in)
		if l != want[0] || v != want[1] {
			t.Errorf("%s -> %s %s", in, l, v)
		}
	}
}

func TestMPMRoundTrip(t *testing.T) {
	p := model.New("My Pack")
	g := p.AddGroup("Performance")
	p.AddMod(&model.Mod{Name: "Sodium", Side: model.SideClient, SideLocked: true, GroupID: g.ID, Notes: "fast",
		File:     model.File{Path: "mods/sodium.jar", SHA1: "a", SHA512: "b", URLs: []string{"https://cdn.modrinth.com/data/A/versions/B/s.jar"}},
		Modrinth: &model.ModrinthRef{ProjectID: "A", VersionID: "B"}})
	p.AddMod(&model.Mod{Name: "JEI", Side: model.SideBoth, CurseForge: &model.CurseForgeRef{ProjectID: 1, FileID: 2}})
	p.Overrides = []*model.OverrideFile{{Side: model.SideServer, Path: "server.properties", Data: []byte("motd=hi")}}
	var buf bytes.Buffer
	if err := WriteMPM(&buf, p); err != nil {
		t.Fatal(err)
	}
	p2, kind, err := Read(buf.Bytes())
	if err != nil || kind != KindMPM {
		t.Fatal(err, kind)
	}
	if p2.Name != "My Pack" || len(p2.Groups) != 1 || p2.Groups[0].Name != "Performance" || len(p2.Mods) != 2 {
		t.Fatalf("%+v", p2)
	}
	s := p2.Mods[0]
	if s.GroupID != g.ID || !s.SideLocked || s.Side != model.SideClient || s.Notes != "fast" || s.Modrinth.VersionID != "B" {
		t.Fatalf("mod lost data %+v", s)
	}
	if len(p2.Overrides) != 1 || string(p2.Overrides[0].Data) != "motd=hi" || p2.Overrides[0].Side != model.SideServer {
		t.Fatalf("overrides %+v", p2.Overrides)
	}
	// The JSON inside is human readable and tagged with the format name.
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	var raw map[string]any
	readJSON(findFile(zr, mpmIndexName), &raw)
	if raw["format"] != "modpackmanager" || raw["formatVersion"].(float64) != 1 {
		t.Fatalf("header %v", raw)
	}
	b, _ := json.Marshal(raw["mods"])
	if !strings.Contains(string(b), `"sideLocked":true`) {
		t.Fatal("sideLocked not serialised")
	}
}

func TestMPMFutureVersionRejected(t *testing.T) {
	data := buildZip(t, map[string]string{mpmIndexName: `{"format":"modpackmanager","formatVersion":99,"name":"x"}`})
	if _, _, err := Read(data); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("expected newer-version error, got %v", err)
	}
}

func TestDetectGarbage(t *testing.T) {
	if _, _, err := Read([]byte("not a zip")); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := Read(buildZip(t, map[string]string{"readme.txt": "x"})); err == nil {
		t.Fatal("expected unrecognised error")
	}
}

func TestCDNParsers(t *testing.T) {
	if id, ok := ParseForgeCDN("https://edge.forgecdn.net/files/4593/548/jei-1.20.1.jar"); !ok || id != 4593548 {
		t.Fatal(id, ok)
	}
	if id, ok := ParseForgeCDN("https://mediafilez.forgecdn.net/files/5000/7/x.jar"); !ok || id != 5000007 {
		t.Fatal(id, ok)
	}
	if got := ForgeCDNURL(4593548, "jei 1.jar"); got != "https://edge.forgecdn.net/files/4593/548/jei%201.jar" {
		t.Fatal(got)
	}
	if _, _, ok := ParseModrinthCDN("https://example.com/data/a/versions/b/c.jar"); ok {
		t.Fatal("non-modrinth host accepted")
	}
}

func TestSaveMPMPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mpm")
	if err := SaveMPM(p, model.New("x")); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode %v", st.Mode().Perm())
	}
	os.Chmod(p, 0o600)
	if err := SaveMPM(p, model.New("x")); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("existing mode not kept: %v", st.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".mpm-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}
