package dale

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type GitAudit struct {
	Root  string          `json:"root"`
	Repos []GitRepoStatus `json:"repos"`
}

type GitRepoStatus struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Branch      string `json:"branch,omitempty"`
	Dirty       int    `json:"dirty"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	HasUpstream bool   `json:"has_upstream"`
	Remote      string `json:"remote,omitempty"`
	State       string `json:"state"`
	Action      string `json:"action,omitempty"`
	Error       string `json:"error,omitempty"`
}

func AuditGitRepos(root string, fetch bool) GitAudit {
	root = filepath.Clean(root)
	audit := GitAudit{Root: root}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		audit.Repos = append(audit.Repos, auditOneRepo(root, fetch))
		return audit
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		status := GitRepoStatus{Name: filepath.Base(root), Path: root, Error: err.Error()}
		status.setState()
		audit.Repos = append(audit.Repos, status)
		return audit
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			continue
		}
		audit.Repos = append(audit.Repos, auditOneRepo(path, fetch))
	}
	sort.Slice(audit.Repos, func(i, j int) bool {
		return strings.ToLower(audit.Repos[i].Name) < strings.ToLower(audit.Repos[j].Name)
	})
	return audit
}

func auditOneRepo(path string, fetch bool) GitRepoStatus {
	status := GitRepoStatus{Name: filepath.Base(path), Path: path}
	if remote, err := gitOutput(path, 5*time.Second, "remote", "get-url", "origin"); err == nil {
		status.Remote = strings.TrimSpace(remote)
	}
	if fetch && status.Remote != "" {
		if _, err := gitOutput(path, 60*time.Second, "fetch", "--all", "--prune", "--quiet"); err != nil {
			status.Error = "fetch: " + err.Error()
		}
	}
	if branch, err := gitOutput(path, 5*time.Second, "branch", "--show-current"); err == nil {
		status.Branch = strings.TrimSpace(branch)
	}
	dirty, err := gitOutput(path, 10*time.Second, "status", "--porcelain=v1")
	if err != nil && status.Error == "" {
		status.Error = "status: " + err.Error()
	}
	status.Dirty = countNonEmptyLines(dirty)
	upstream, err := gitOutput(path, 5*time.Second, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil || strings.TrimSpace(upstream) == "" {
		status.HasUpstream = false
		status.setState()
		return status
	}
	status.HasUpstream = true
	counts, err := gitOutput(path, 10*time.Second, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		if status.Error == "" {
			status.Error = "ahead-behind: " + err.Error()
		}
		return status
	}
	parts := strings.Fields(counts)
	if len(parts) >= 2 {
		status.Ahead, _ = strconv.Atoi(parts[0])
		status.Behind, _ = strconv.Atoi(parts[1])
	}
	status.setState()
	return status
}

func (status *GitRepoStatus) setState() {
	switch {
	case status.Error != "":
		status.State = "error"
		status.Action = "inspect repo manually"
	case status.Dirty > 0 && status.Behind > 0:
		status.State = "dirty-behind"
		status.Action = "commit or stash local changes, then pull"
	case status.Dirty > 0 && status.Ahead > 0:
		status.State = "dirty-ahead"
		status.Action = "commit or stash local changes, then push"
	case status.Dirty > 0:
		status.State = "dirty"
		status.Action = "commit or stash local changes"
	case !status.HasUpstream:
		status.State = "no-upstream"
		status.Action = "set or repair the upstream branch"
	case status.Ahead > 0 && status.Behind > 0:
		status.State = "diverged"
		status.Action = "review, rebase or merge, then push"
	case status.Ahead > 0:
		status.State = "ahead"
		status.Action = "push"
	case status.Behind > 0:
		status.State = "behind"
		status.Action = "pull"
	default:
		status.State = "clean"
	}
}

func gitOutput(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

func countNonEmptyLines(s string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
