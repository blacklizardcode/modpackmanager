package jarmeta

import (
	"archive/zip"
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"modpackmanager/internal/model"
)

// jar builds a zip in memory. Iteration order doesn't matter to the reader.
func jar(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readJar(t *testing.T, files map[string]string) *Info {
	t.Helper()
	info, err := Read(jar(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestFabric(t *testing.T) {
	// A raw line break inside a string and §-codes are common in the wild.
	info := readJar(t, map[string]string{
		"fabric.mod.json": "\xef\xbb\xbf" + `{
			"schemaVersion": 1, "id": "examplemod", "version": "1.2.3+mc1.21.1",
			"name": "§aExample§r Mod", "description": "Line one
line two",
			"authors": ["Alice", {"name": "Bob", "contact": {}}, "alice"],
			"environment": "client",
			"icon": {"16": "assets/examplemod/icon16.png", "128": "assets/examplemod/icon.png"}
		}`,
		"assets/examplemod/icon.png": "PNGDATA",
	})
	want := &Info{
		ID: "examplemod", Name: "Example Mod", Version: "1.2.3+mc1.21.1", Description: "Line one\nline two",
		Authors: []string{"Alice", "Bob"}, Loaders: []string{Fabric}, Side: model.SideClient,
		IconPath: "assets/examplemod/icon.png",
	}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("got  %+v\nwant %+v", info, want)
	}
}

func TestFabricSides(t *testing.T) {
	for env, want := range map[string]model.Side{
		`"environment": "server",`: model.SideServer,
		`"environment": "*",`:      model.SideBoth,
		``:                         model.SideBoth, // missing means "everywhere"
	} {
		info := readJar(t, map[string]string{"fabric.mod.json": `{` + env + `"id": "x", "version": 2}`})
		if info.Side != want || info.Version != "2" {
			t.Errorf("%q: side %v version %q", env, info.Side, info.Version)
		}
	}
}

func TestQuilt(t *testing.T) {
	info := readJar(t, map[string]string{"quilt.mod.json": `{
		"schema_version": 1,
		"quilt_loader": {
			"group": "org.example", "id": "qmod", "version": "0.4.0",
			"metadata": {"name": "Q Mod", "description": "Quilted", "contributors": {"Zed": "Owner", "Amy": ["Developer"]}, "icon": "assets/qmod/icon.png"}
		},
		"minecraft": {"environment": "dedicated_server"}
	}`})
	if info.ID != "qmod" || info.Name != "Q Mod" || info.Version != "0.4.0" || info.Side != model.SideServer ||
		!reflect.DeepEqual(info.Authors, []string{"Amy", "Zed"}) || !reflect.DeepEqual(info.Loaders, []string{Quilt}) ||
		info.IconPath != "assets/qmod/icon.png" {
		t.Fatalf("%+v", info)
	}
}

const forgeTOML = `# A typical Forge mods.toml
modLoader="javafml" #mandatory
loaderVersion="[47,)"
license='All rights reserved'
logoFile="top-logo.png"

[[mods]] # the mod
modId="examplemod"
version="${file.jarVersion}"
displayName="Example Mod" # name
authors="Alice, Bob"
description='''
An example.
With two lines.
'''

[[dependencies.examplemod]]
    modId="forge"
    mandatory=true
    versionRange="[47,)"
    ordering="NONE"
    side="BOTH"

[modproperties.examplemod]
    displayName="should be ignored"
`

func TestForge(t *testing.T) {
	info := readJar(t, map[string]string{
		"META-INF/mods.toml":   forgeTOML,
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\r\nImplementation-Title: examplemod\r\nImplementation-Version: 4.5.6-bu\r\n ild7\r\n\r\n",
	})
	want := &Info{
		ID: "examplemod", Name: "Example Mod", Version: "4.5.6-build7", Description: "An example.\nWith two lines.",
		Authors: []string{"Alice, Bob"}, Loaders: []string{Forge}, Side: model.SideUnknown, IconPath: "top-logo.png",
	}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("got  %+v\nwant %+v", info, want)
	}
}

func TestNeoForge(t *testing.T) {
	// NeoForge 1.20.2–1.20.4 still used mods.toml; its mods depend on "neoforge".
	old := strings.Replace(forgeTOML, `modId="forge"`, `modId="neoforge"`, 1)
	info := readJar(t, map[string]string{"META-INF/mods.toml": old})
	if !reflect.DeepEqual(info.Loaders, []string{NeoForge}) || info.Version != "" {
		t.Fatalf("%+v", info)
	}
	info = readJar(t, map[string]string{"META-INF/neoforge.mods.toml": `[[mods]]
modId = "neo"
version = "2.0"
displayName = "Neo"`})
	if info.ID != "neo" || info.Version != "2.0" || !reflect.DeepEqual(info.Loaders, []string{NeoForge}) {
		t.Fatalf("%+v", info)
	}
}

func TestMultiLoaderJar(t *testing.T) {
	info := readJar(t, map[string]string{
		"fabric.mod.json":    `{"id": "both", "name": "Both Ways", "version": "1.0", "environment": "*"}`,
		"META-INF/mods.toml": "[[mods]]\nmodId=\"both\"\ndisplayName=\"Other Name\"\n",
	})
	if info.Name != "Both Ways" || !reflect.DeepEqual(info.Loaders, []string{Fabric, Forge}) || info.Side != model.SideBoth {
		t.Fatalf("%+v", info)
	}
}

func TestMcmodInfo(t *testing.T) {
	for _, body := range []string{
		`[{"modid": "old", "name": "Old Mod", "version": "1.12.2-1", "authorList": ["Ann"], "logoFile": "/logo.png"}]`,
		`{"modListVersion": 2, "modList": [{"modid": "old", "name": "Old Mod", "version": "1.12.2-1", "authorList": ["Ann"], "logoFile": "/logo.png"}]}`,
	} {
		info := readJar(t, map[string]string{"mcmod.info": body})
		if info.ID != "old" || info.Name != "Old Mod" || info.Version != "1.12.2-1" ||
			!reflect.DeepEqual(info.Authors, []string{"Ann"}) || info.IconPath != "logo.png" || !reflect.DeepEqual(info.Loaders, []string{Forge}) {
			t.Fatalf("%s: %+v", body, info)
		}
	}
}

func TestNoMetadataAndBrokenMetadata(t *testing.T) {
	info := readJar(t, map[string]string{"com/example/Lib.class": "\xca\xfe\xba\xbe"})
	if len(info.Loaders) != 0 || info.Name != "" || info.Side != model.SideUnknown {
		t.Fatalf("plain jar: %+v", info)
	}
	// Unparseable JSON still tells us which loader the jar is for.
	info = readJar(t, map[string]string{"fabric.mod.json": `{"id": "x",,}`})
	if !reflect.DeepEqual(info.Loaders, []string{Fabric}) || info.ID != "" {
		t.Fatalf("broken json: %+v", info)
	}
	// Unexpanded build placeholders are dropped rather than shown.
	info = readJar(t, map[string]string{"fabric.mod.json": `{"id": "x", "version": "${version}"}`})
	if info.Version != "" {
		t.Fatalf("placeholder kept: %+v", info)
	}
}

func TestNotAJar(t *testing.T) {
	if _, err := Read([]byte("definitely not a zip")); !errors.Is(err, ErrNotJar) {
		t.Fatalf("got %v", err)
	}
	if Icon([]byte("nope")) != nil {
		t.Fatal("icon from garbage")
	}
}

func TestIcon(t *testing.T) {
	data := jar(t, map[string]string{
		"fabric.mod.json":            `{"id": "x", "icon": "/assets/x/icon.png"}`,
		"assets/x/icon.png":          "the-icon",
		"META-INF/mods.toml":         "[[mods]]\nlogoFile=\"other.png\"\n",
		"other.png":                  "not this one",
		"assets/x/unrelated_big.png": strings.Repeat("x", 100),
	})
	if got := string(Icon(data)); got != "the-icon" {
		t.Fatalf("icon %q", got)
	}
	if Icon(jar(t, map[string]string{"fabric.mod.json": `{"id": "x", "icon": "missing.png"}`})) != nil {
		t.Fatal("missing icon file should give nil")
	}
}

func TestLongTextIsCapped(t *testing.T) {
	info := readJar(t, map[string]string{"fabric.mod.json": `{"id": "x", "name": "` + strings.Repeat("n", 500) + `", "description": "` + strings.Repeat("d", 9000) + `"}`})
	if len([]rune(info.Name)) != maxName+1 || len([]rune(info.Description)) != maxDesc+1 {
		t.Fatalf("name %d, description %d runes", len([]rune(info.Name)), len([]rune(info.Description)))
	}
}

func TestParseTOML(t *testing.T) {
	doc := parseTOML(`
a = "basic \"q\" é \\ tab\t"
b = 'literal \n stays'
c = """
multi \
    joined "quoted" ""ok"""
d = '''
raw\n'''
e = [ "x", 'y', # comment
  3, [ "nested" ], { inline = 1 } ]
f = 42 # trailing comment
g = { skipped = "yes" }
"quoted.key" = "q"
a = "duplicate keys keep the first value"
[[mods]]
modId = "m1"
[ plain.table ]
ignored = "x"
[[mods]]
modId = "m2"
[[dependencies."m1"]]
modId = "forge"
`)
	wantTop := map[string]string{
		"a": "basic \"q\" é \\ tab\t", "b": `literal \n stays`, "c": `multi joined "quoted" ""ok`,
		"d": `raw\n`, "e": "x, y, 3, nested", "f": "42", "quoted.key": "q",
	}
	if !reflect.DeepEqual(doc.top, wantTop) {
		t.Errorf("top\ngot  %q\nwant %q", doc.top, wantTop)
	}
	mods := doc.arrays["mods"]
	if len(mods) != 2 || mods[0]["modId"] != "m1" || mods[1]["modId"] != "m2" || len(mods[0]) != 1 {
		t.Errorf("mods %q", mods)
	}
	if deps := doc.arrays["dependencies.m1"]; len(deps) != 1 || deps[0]["modId"] != "forge" {
		t.Errorf("deps %q", doc.arrays)
	}
}

func TestParseTOMLNeverHangs(t *testing.T) {
	for _, s := range []string{
		"", "=", "a", "a =", `a = "unterminated`, `a = """unterminated`, "a = '''x", "a = [", "a = [1, 2",
		"[[", "[[mods", "[x]]", `a = "\`, `a = "\u12"`, "a = {", "a = { b = { c = 1 }", "a = [ { ] }",
		"a = [\"x\n\"]", "\x00\xff = \x00", strings.Repeat("[", 1000), strings.Repeat(`"`, 1000),
	} {
		parseTOML(s) // must return
	}
}

func FuzzParseTOML(f *testing.F) {
	f.Add(forgeTOML)
	f.Add(`a = """x\""" y"""` + "\n" + `b = [1, "2", '3']`)
	f.Fuzz(func(t *testing.T, s string) {
		parseTOML(s)
	})
}

func FuzzRead(f *testing.F) {
	f.Add(jar(f, map[string]string{"fabric.mod.json": `{"id": "x", "icon": {"32": "i.png"}}`, "i.png": "i"}))
	f.Add(jar(f, map[string]string{"META-INF/mods.toml": forgeTOML}))
	f.Add(jar(f, map[string]string{"quilt.mod.json": `{"quilt_loader": {"id": "q"}}`, "mcmod.info": `[{"modid": "m"}]`}))
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Read(data)
		if err == nil && info == nil {
			t.Fatal("nil info without error")
		}
		Icon(data)
	})
}
