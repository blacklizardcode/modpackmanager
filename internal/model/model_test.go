package model

import "testing"

func TestSideFromEnv(t *testing.T) {
	cases := []struct {
		c, s string
		want Side
	}{
		{"required", "unsupported", SideClient},
		{"optional", "unsupported", SideClient},
		{"unsupported", "required", SideServer},
		{"required", "required", SideBoth},
		{"optional", "optional", SideBoth},
		{"required", "optional", SideClient},
		{"optional", "required", SideServer},
		{"unknown", "unknown", SideUnknown},
		{"", "", SideUnknown},
		{"unsupported", "unsupported", SideUnknown},
	}
	for _, c := range cases {
		if got := SideFromEnv(c.c, c.s); got != c.want {
			t.Errorf("SideFromEnv(%q,%q)=%v want %v", c.c, c.s, got, c.want)
		}
	}
}

func TestSideFromModrinthEnvironment(t *testing.T) {
	cases := map[string]Side{
		"client_only": SideClient, "client_only_server_optional": SideClient, "singleplayer_only": SideClient,
		"server_only": SideServer, "dedicated_server_only": SideServer, "server_only_client_optional": SideServer,
		"client_and_server": SideBoth, "client_or_server": SideBoth, "client_or_server_prefers_both": SideBoth,
	}
	for in, want := range cases {
		if got, ok := SideFromModrinthEnvironment(in); !ok || got != want {
			t.Errorf("%s -> %v %v", in, got, ok)
		}
	}
	if _, ok := SideFromModrinthEnvironment("unknown"); ok {
		t.Error("unknown should not be ok")
	}
}

func TestEnvForSideRoundTrip(t *testing.T) {
	for _, s := range []Side{SideClient, SideServer, SideBoth} {
		c, sv := EnvForSide(s)
		if SideFromEnv(c, sv) != s {
			t.Errorf("round trip %v", s)
		}
	}
}

func TestGroupsAndSections(t *testing.T) {
	p := New("x")
	g1 := p.AddGroup("Performance")
	g2 := p.AddGroup("Visual")
	if p.AddGroup("performance") != g1 {
		t.Fatal("duplicate group created")
	}
	a := &Mod{Name: "b-mod", GroupID: g1.ID}
	b := &Mod{Name: "A-mod", GroupID: g1.ID}
	c := &Mod{Name: "loose"}
	for _, m := range []*Mod{a, b, c} {
		if !p.AddMod(m) {
			t.Fatal("add failed")
		}
	}
	secs := p.Sections(nil)
	if len(secs) != 3 || secs[0].Group != g1 || secs[1].Group != g2 || secs[2].Group != nil {
		t.Fatalf("sections %+v", secs)
	}
	if secs[0].Mods[0] != b {
		t.Fatal("not sorted by name")
	}
	p.MoveGroup(g2.ID, -1)
	if p.Groups[0] != g2 {
		t.Fatal("move failed")
	}
	p.RemoveGroup(g1.ID)
	if a.GroupID != "" || len(p.Groups) != 1 {
		t.Fatal("remove group didn't ungroup mods")
	}
}

func TestDuplicates(t *testing.T) {
	p := New("x")
	p.AddMod(&Mod{Name: "a", Modrinth: &ModrinthRef{ProjectID: "P"}})
	if p.AddMod(&Mod{Name: "a2", Modrinth: &ModrinthRef{ProjectID: "P"}}) {
		t.Fatal("dup modrinth")
	}
	p.AddMod(&Mod{Name: "b", CurseForge: &CurseForgeRef{ProjectID: 5}})
	if p.AddMod(&Mod{Name: "b2", CurseForge: &CurseForgeRef{ProjectID: 5, FileID: 9}}) {
		t.Fatal("dup cf")
	}
	p.AddMod(&Mod{Name: "c", File: File{SHA1: "ABC"}})
	if p.AddMod(&Mod{Name: "c2", File: File{SHA1: "abc"}}) {
		t.Fatal("dup hash")
	}
	if len(p.Mods) != 3 {
		t.Fatal(len(p.Mods))
	}
}

func TestDisplayName(t *testing.T) {
	m := &Mod{File: File{Path: "mods/sodium-0.5.jar"}}
	if m.DisplayName() != "sodium-0.5" {
		t.Fatal(m.DisplayName())
	}
	if (&Mod{}).DisplayName() == "" {
		t.Fatal("empty")
	}
}
