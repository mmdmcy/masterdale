package dale

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAuditGitRepos(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	audit := AuditGitRepos(root, false)
	if len(audit.Repos) != 1 {
		t.Fatalf("expected one repo: %#v", audit)
	}
	if audit.Repos[0].Dirty == 0 {
		t.Fatalf("expected dirty repo: %#v", audit.Repos[0])
	}
	if audit.Repos[0].State != "dirty" {
		t.Fatalf("expected dirty state: %#v", audit.Repos[0])
	}
	rootAudit := AuditGitRepos(repo, false)
	if len(rootAudit.Repos) != 1 {
		t.Fatalf("expected direct repo audit: %#v", rootAudit)
	}
	if rootAudit.Repos[0].Name != "repo" {
		t.Fatalf("unexpected repo name: %#v", rootAudit.Repos[0])
	}
}
