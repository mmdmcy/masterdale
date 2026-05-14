package dale

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type NPMScanResult struct {
	Roots    []string       `json:"roots"`
	Packages []string       `json:"packages"`
	Files    int            `json:"files"`
	Matches  []NPMScanMatch `json:"matches"`
	Skipped  []string       `json:"skipped,omitempty"`
}

type NPMScanMatch struct {
	Path    string   `json:"path"`
	Package string   `json:"package"`
	Lines   []string `json:"lines,omitempty"`
}

func ScanNPM(roots []string, packages []string, maxFiles int) NPMScanResult {
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	packages = cleanPackages(packages)
	result := NPMScanResult{Roots: roots, Packages: packages}
	for _, root := range roots {
		root = filepath.Clean(root)
		if _, err := os.Stat(root); err != nil {
			result.Skipped = append(result.Skipped, root+": "+err.Error())
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				result.Skipped = append(result.Skipped, path+": "+err.Error())
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				switch name {
				case ".git", "node_modules", "dist", "build", ".next", ".cache":
					return filepath.SkipDir
				}
				return nil
			}
			if !isPackageFile(name) {
				return nil
			}
			result.Files++
			if result.Files > maxFiles {
				return filepath.SkipAll
			}
			b, err := os.ReadFile(path)
			if err != nil {
				result.Skipped = append(result.Skipped, path+": "+err.Error())
				return nil
			}
			text := string(b)
			if len(packages) == 0 {
				return nil
			}
			lines := strings.Split(text, "\n")
			for _, pkg := range packages {
				if !strings.Contains(text, pkg) {
					continue
				}
				match := NPMScanMatch{Path: path, Package: pkg}
				for _, line := range lines {
					if strings.Contains(line, pkg) {
						match.Lines = append(match.Lines, Shorten(strings.TrimSpace(line), 240))
						if len(match.Lines) >= 5 {
							break
						}
					}
				}
				result.Matches = append(result.Matches, match)
			}
			return nil
		})
	}
	return result
}

func isPackageFile(name string) bool {
	switch name {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb":
		return true
	default:
		return false
	}
}

func cleanPackages(packages []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" && !seen[pkg] {
			seen[pkg] = true
			out = append(out, pkg)
		}
	}
	return out
}
