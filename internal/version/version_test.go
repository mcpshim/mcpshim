package version

import "testing"

func TestDevelopmentVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty")
	}
}
