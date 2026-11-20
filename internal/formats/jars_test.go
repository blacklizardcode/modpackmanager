package formats

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"modpackmanager/internal/jarmeta"
	"modpackmanager/internal/model"
)

func fabricJar(t *testing.T, id, env string) []byte {
	t.Helper()
	return buildZip(t, map[string]string{
		"fabric.mod.json": `{"schemaVersion": 1, "id": "` + id + `", "name": "Mod ` + id + `", "version": "1.0", "environment": "` + env + `", "authors": ["Ann"]}`,
	})
}

func zipNames(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		b, err := readAll(f)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func TestLocalMod(t *testing.T) {
	data := fabricJar(t, "mine", "client")
	info, err := jarmeta.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	m := LocalMod("mods/mine-1.0.jar", data, info)
	if m.Name != "Mod mine" || m.Author != "Ann" || m.Side != model.SideClient || m.SideLocked ||
		m.File.Size != int64(len(data)) || len(m.File.SHA1) != 40 || len(m.File.SHA512) != 128 ||
		m.Local == nil || m.Local.ModID != "mine" || m.Local.Version != "1.0" || m.PrimarySource() != model.SourceLocal ||
		m.SourceLabel() != "Local file" {
		t.Fatalf("%+v %+v", m, m.Local)
	}
	// No metadata at all: named after the file.
	m = LocalMod("mods/Some Lib-2.jar", buildZip(t, map[string]string{"a.class": "x"}), &jarmeta.Info{Side: model.SideUnknown})
	if m.Name != "Some Lib-2" || m.Local != nil || m.Side != model.SideUnknown {
		t.Fatalf("%+v", m)
	}
}

func TestMPMStoresJars(t *testing.T) {
	data := fabricJar(t, "mine", "*")
	info, _ := jarmeta.Read(data)
	p := model.New("Jars")
	m := LocalMod("mods/mine.jar", data, info)
	m.Notes = "my build"
	p.AddMod(m)
	var buf bytes.Buffer
	if err := WriteMPM(&buf, p); err != nil {
		t.Fatal(err)
	}
	files := zipNames(t, buf.Bytes())
	stored := "files/" + m.File.SHA1 + "/mine.jar"
	if !bytes.Equal(files[stored], data) {
		t.Fatalf("stored jar missing; entries: %v", keysOf(files))
	}
	if !strings.Contains(string(files[mpmIndexName]), `"formatVersion": 2`) {
		t.Fatal("a pack with stored jars must be format version 2")
	}
	if strings.Contains(string(files[mpmIndexName]), `"data"`) {
		t.Fatal("jar bytes leaked into the JSON")
	}
	p2, _, err := Read(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Mods) != 1 || len(p2.Overrides) != 0 {
		t.Fatalf("mods %d overrides %d", len(p2.Mods), len(p2.Overrides))
	}
	got := p2.Mods[0]
	if !bytes.Equal(got.File.Data, data) || got.Notes != "my build" || got.Local == nil || got.Local.ModID != "mine" || got.Side != model.SideBoth {
		t.Fatalf("%+v", got)
	}
}

func TestMPMRejectsDamagedStoredJar(t *testing.T) {
	data := fabricJar(t, "mine", "*")
	m := LocalMod("mods/mine.jar", data, nil)
	pack := buildZip(t, map[string]string{
		mpmIndexName:                         `{"format":"modpackmanager","formatVersion":2,"name":"x","mods":[{"id":"a","name":"x","side":"both","file":{"path":"mods/mine.jar","sha1":"` + m.File.SHA1 + `"}}]}`,
		"files/" + m.File.SHA1 + "/mine.jar": "something else",
	})
	if _, _, err := Read(pack); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("expected a damaged-file error, got %v", err)
	}
	// Writing refuses bytes that don't match the recorded hash.
	p := model.New("x")
	m.File.Data = []byte("changed")
	p.AddMod(m)
	if err := WriteMPM(&bytes.Buffer{}, p); err == nil {
		t.Fatal("expected a hash mismatch error")
	}
}

func TestMrpackOverrideJarsBecomeMods(t *testing.T) {
	client := fabricJar(t, "zoom", "client")
	plain := buildZip(t, map[string]string{"lib/Thing.class": "x"})
	pack := buildZip(t, map[string]string{
		"modrinth.index.json":          sampleIndex,
		"overrides/mods/zoom.jar":      string(client),                 // metadata says client
		"overrides/mods/plain.JAR":     string(plain),                  // no metadata: both, like overrides/
		"server-overrides/mods/s.jar":  string(fabricJar(t, "s", "*")), // folder wins over metadata
		"overrides/mods/sub/deep.jar":  string(plain),                  // not directly in mods/
		"overrides/mods/broken.jar":    "not a zip",                    // stays an override
		"overrides/mods/sodium.jar":    string(plain),                  // clashes with a manifest entry
		"overrides/config/zoom.json5":  "{}",
		"client-overrides/options.txt": "fov:90",
	})
	p, _, err := Read(pack)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*model.Mod{}
	for _, m := range p.Mods {
		byPath[m.File.Path] = m
	}
	if m := byPath["mods/zoom.jar"]; m == nil || m.Side != model.SideClient || m.SideLocked || !bytes.Equal(m.File.Data, client) || m.Name != "Mod zoom" {
		t.Fatalf("zoom: %+v", m)
	}
	if m := byPath["mods/plain.JAR"]; m == nil || m.Side != model.SideBoth || m.Name != "plain" {
		t.Fatalf("plain: %+v", m)
	}
	if m := byPath["mods/s.jar"]; m == nil || m.Side != model.SideServer || !m.SideLocked {
		t.Fatalf("server-overrides jar: %+v", m)
	}
	var left []string
	for _, o := range p.Overrides {
		left = append(left, string(o.Side)+":"+o.Path)
	}
	want := "both:config/zoom.json5 both:mods/broken.jar both:mods/sodium.jar both:mods/sub/deep.jar client:options.txt"
	if strings.Join(left, " ") != want {
		t.Fatalf("overrides left:\n got %s\nwant %s", strings.Join(left, " "), want)
	}
	if len(p.Mods) != 4+3 {
		t.Fatalf("%d mods", len(p.Mods))
	}
}

func TestCurseForgeOverrideJarsBecomeMods(t *testing.T) {
	pack := buildZip(t, map[string]string{
		"manifest.json":         `{"minecraft":{"version":"1.20.1","modLoaders":[{"id":"forge-47.2.0","primary":true}]},"manifestType":"minecraftModpack","name":"CF","files":[{"projectID":1,"fileID":2,"required":true}],"overrides":"extra"}`,
		"extra/mods/local.jar":  string(buildZip(t, map[string]string{"META-INF/mods.toml": "[[mods]]\nmodId=\"loc\"\ndisplayName=\"Local One\"\nversion=\"3\"\n"})),
		"extra/config/a.cfg":    "x",
		"overrides/mods/no.jar": string(fabricJar(t, "no", "*")), // not the pack's overrides folder
	})
	p, _, err := Read(pack)
	if err != nil {
		t.Fatal(err)
	}
	var local *model.Mod
	for _, m := range p.Mods {
		if m.PrimarySource() == model.SourceLocal {
			local = m
		}
	}
	if local == nil || local.Name != "Local One" || local.Side != model.SideBoth || local.Local.Version != "3" ||
		local.Local.Loaders[0] != jarmeta.Forge || len(p.Mods) != 2 || len(p.Overrides) != 1 {
		t.Fatalf("local %+v, %d mods, %d overrides", local, len(p.Mods), len(p.Overrides))
	}
}

func TestOldMPMWithOverrideJarUpgrades(t *testing.T) {
	jar := fabricJar(t, "old", "*")
	v1 := buildZip(t, map[string]string{
		mpmIndexName:                  `{"format":"modpackmanager","formatVersion":1,"name":"Old","mods":[]}`,
		"client-overrides/mods/x.jar": string(jar),
	})
	p, _, err := Read(v1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Mods) != 1 || p.Mods[0].Side != model.SideClient || len(p.Overrides) != 0 {
		t.Fatalf("%+v / %d overrides", p.Mods, len(p.Overrides))
	}
	var buf bytes.Buffer
	if err := WriteMPM(&buf, p); err != nil {
		t.Fatal(err)
	}
	files := zipNames(t, buf.Bytes())
	if _, ok := files["client-overrides/mods/x.jar"]; ok {
		t.Fatal("jar written twice")
	}
	if !bytes.Equal(files["files/"+p.Mods[0].File.SHA1+"/x.jar"], jar) {
		t.Fatalf("entries %v", keysOf(files))
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
