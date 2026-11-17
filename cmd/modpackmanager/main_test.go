package main

import "testing"

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
