package comdale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(path, []byte(`{"name":"Demo","audience":["founders"],"offers":["AI"],"approval_required":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Demo" || !profile.ApprovalRequired {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestDefaultProfileIsReusableExample(t *testing.T) {
	profile := DefaultProfile()
	if profile.Name != "Example Business" {
		t.Fatalf("default profile should be generic, got %#v", profile)
	}
	if len(profile.RepoPaths) == 0 {
		t.Fatal("expected reusable default repo paths")
	}
}
