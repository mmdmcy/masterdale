package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("TEST_TOKEN=from-file\nQUOTED=\"ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_TOKEN", "from-env")
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TEST_TOKEN"); got != "from-env" {
		t.Fatalf("expected existing env to win, got %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "ok" {
		t.Fatalf("expected quoted value, got %q", got)
	}
}
