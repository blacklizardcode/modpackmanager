package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modpackmanager/internal/core"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
)

func TestPreferredUI(t *testing.T) {
	cases := []struct{ goos, desktop, forced, want string }{
		{"linux", "GNOME", "", "gtk"},
		{"linux", "ubuntu:GNOME", "", "gtk"},
		{"linux", "KDE", "", "qt"},
		{"linux", "LXQt", "", "qt"},
		{"linux", "XFCE", "", "gtk"},
		{"linux", "", "", "gtk"},
		{"linux", "GNOME", "qt", "qt"},
		{"linux", "KDE", "GTK", "gtk"},
		{"windows", "", "", "qt"},
		{"darwin", "", "", "qt"},
		{"freebsd", "KDE", "", "qt"},
	}
	for _, c := range cases {
		if got := PreferredUI(c.goos, c.desktop, c.forced); got != c.want {
			t.Errorf("%+v -> %s", c, got)
		}
	}
}

func TestCmdAdd(t *testing.T) {
	t.Setenv("MPM_CONFIG_DIR", t.TempDir())
	t.Setenv("MPM_CACHE_DIR", t.TempDir())
	dir := t.TempDir()
	pack := filepath.Join(dir, "p.mpm")
	if err := formats.SaveMPM(pack, model.New("P")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("fabric.mod.json")
	w.Write([]byte(`{"schemaVersion": 1, "id": "cli", "name": "CLI Mod", "version": "1.0"}`))
	zw.Close()
	jar := filepath.Join(dir, "cli-1.0.jar")
	if err := os.WriteFile(jar, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{pack, jar, "--side", "client", "--offline"}); err != nil {
		t.Fatal(err)
	}
	doc, err := core.Open(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pack.Mods) != 1 {
		t.Fatalf("%d mods", len(doc.Pack.Mods))
	}
	m := doc.Pack.Mods[0]
	if m.Name != "CLI Mod" || m.Side != model.SideClient || !m.SideLocked || !bytes.Equal(m.File.Data, buf.Bytes()) {
		t.Fatalf("%+v", m)
	}
	// Unreadable files fail the command but keep what worked.
	if err := cmdAdd([]string{pack, filepath.Join(dir, "missing.jar"), "--offline"}); err == nil || !strings.Contains(err.Error(), "1 of 1 files") {
		t.Fatalf("got %v", err)
	}
	for _, args := range [][]string{
		{pack, jar, "--side", "sideways"},
		{pack, jar, "--side"},
		{pack, jar, "--bogus"},
		{pack},
	} {
		if err := cmdAdd(args); err == nil {
			t.Errorf("%q: expected an error", args)
		}
	}
	mrpack := filepath.Join(dir, "p.mrpack")
	os.WriteFile(mrpack, []byte("x"), 0o644)
	if err := cmdAdd([]string{mrpack, jar}); err == nil {
		t.Error("adding to a non-.mpm file must fail")
	}
}

func TestCmdMCPRejectsUnknownOptions(t *testing.T) {
	stdout := os.Stdout
	err := cmdMCP([]string{"--readonly"})
	if err == nil || !strings.Contains(err.Error(), "--read-only") {
		t.Fatalf("got %v", err)
	}
	if os.Stdout != stdout {
		t.Fatal("stdout was redirected before the options were checked")
	}
}
