package version

import (
	"regexp"
	"testing"
)

// The release workflow and the Nix package read the VERSION file too, so a
// malformed edit should fail here first.
func TestString(t *testing.T) {
	re := regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)
	if v := String(); !re.MatchString(v) {
		t.Fatalf("VERSION %q is not like 1.2.3 or 1.2.3-rc.1", v)
	}
}
