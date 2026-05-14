package comdale

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type RepoSummary struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scripts     []string `json:"scripts"`
	Exists      bool     `json:"exists"`
}

func ScanRepos(profile BusinessProfile, baseDir string) []RepoSummary {
	var out []RepoSummary
	for _, p := range profile.RepoPaths {
		path := p
		if !filepath.IsAbs(path) && baseDir != "" {
			path = filepath.Clean(filepath.Join(baseDir, path))
		}
		summary := RepoSummary{Path: path}
		if _, err := os.Stat(path); err == nil {
			summary.Exists = true
			summary.Name = filepath.Base(path)
			if b, err := os.ReadFile(filepath.Join(path, "README.md")); err == nil {
				summary.Description = firstHeadingOrLine(string(b))
			}
			if b, err := os.ReadFile(filepath.Join(path, "package.json")); err == nil {
				var pkg struct {
					Name    string            `json:"name"`
					Scripts map[string]string `json:"scripts"`
				}
				if json.Unmarshal(b, &pkg) == nil {
					if pkg.Name != "" {
						summary.Name = pkg.Name
					}
					for key := range pkg.Scripts {
						summary.Scripts = append(summary.Scripts, key)
					}
				}
			}
		}
		out = append(out, summary)
	}
	return out
}

func firstHeadingOrLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return ""
}
