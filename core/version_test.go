package core

import (
	"regexp"
	"testing"
)

var versionTestSemver = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
	if !versionTestSemver.MatchString(Version) {
		t.Errorf("Version = %q, want a semantic version", Version)
	}
}
