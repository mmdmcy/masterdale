package dale

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

type FileReadResult struct {
	Path      string `json:"path"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type FileSearchResult struct {
	Root    string            `json:"root"`
	Query   string            `json:"query"`
	Files   int               `json:"files"`
	Matches []FileSearchMatch `json:"matches"`
	Skipped []string          `json:"skipped,omitempty"`
}

type FileSearchMatch struct {
	Path  string   `json:"path"`
	Lines []string `json:"lines"`
}

func ListFiles(safeRoots []string, requested string) ([]FileEntry, error) {
	path, err := safePath(safeRoots, requested)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if isBlockedFileName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func ReadFileSafe(safeRoots []string, requested string, maxBytes int64) (FileReadResult, error) {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	if maxBytes > 2*1024*1024 {
		maxBytes = 2 * 1024 * 1024
	}
	path, err := safePath(safeRoots, requested)
	if err != nil {
		return FileReadResult{}, err
	}
	if isBlockedFileName(filepath.Base(path)) {
		return FileReadResult{}, errors.New("refusing to read secret or runtime file")
	}
	info, err := os.Stat(path)
	if err != nil {
		return FileReadResult{}, err
	}
	if info.IsDir() {
		return FileReadResult{}, errors.New("path is a directory")
	}
	f, err := os.Open(path)
	if err != nil {
		return FileReadResult{}, err
	}
	defer f.Close()
	buf := make([]byte, maxBytes+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return FileReadResult{}, err
	}
	data := buf[:n]
	truncated := int64(n) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	result := FileReadResult{Path: path, Size: info.Size(), Truncated: truncated}
	if utf8.Valid(data) {
		result.Encoding = "utf-8"
		result.Content = string(data)
	} else {
		result.Encoding = "base64"
		result.Content = base64.StdEncoding.EncodeToString(data)
	}
	return result, nil
}

func SearchFiles(safeRoots []string, root string, query string, maxFiles int, maxMatches int) FileSearchResult {
	query = strings.TrimSpace(query)
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	if maxMatches <= 0 {
		maxMatches = 100
	}
	resolved, err := safePath(safeRoots, root)
	result := FileSearchResult{Root: root, Query: query}
	if err != nil {
		result.Skipped = append(result.Skipped, err.Error())
		return result
	}
	result.Root = resolved
	if query == "" {
		result.Skipped = append(result.Skipped, "query is required")
		return result
	}
	needle := strings.ToLower(query)
	_ = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			result.Skipped = append(result.Skipped, path+": "+err.Error())
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "build", ".next", ".cache", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if isBlockedFileName(d.Name()) {
			return nil
		}
		result.Files++
		if result.Files > maxFiles || len(result.Matches) >= maxMatches {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1024*1024 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(b) {
			return nil
		}
		text := string(b)
		if !strings.Contains(strings.ToLower(path+"\n"+text), needle) {
			return nil
		}
		match := FileSearchMatch{Path: path}
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), needle) {
				match.Lines = append(match.Lines, Shorten(strings.TrimSpace(line), 240))
				if len(match.Lines) >= 8 {
					break
				}
				if i == 0 && strings.TrimSpace(line) == "" {
					break
				}
			}
		}
		if len(match.Lines) == 0 {
			match.Lines = []string{"matched path"}
		}
		result.Matches = append(result.Matches, match)
		return nil
	})
	return result
}

func isBlockedFileName(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return true
	}
	switch lower {
	case "config.json", "auth.json", "events.jsonl", "tasks.jsonl", "metrics.jsonl":
		return true
	default:
		return false
	}
}

func safePath(safeRoots []string, requested string) (string, error) {
	if len(safeRoots) == 0 {
		return "", errors.New("no safe roots configured")
	}
	base, err := filepath.Abs(safeRoots[0])
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(requested)
	if target == "" {
		target = base
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	for _, root := range safeRoots {
		absRoot, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		if isWithin(absRoot, target) {
			return target, nil
		}
	}
	return "", errors.New("path is outside configured safe roots")
}

func isWithin(root string, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	outside := rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
	if runtime.GOOS == "windows" {
		return !outside && (strings.HasPrefix(strings.ToLower(target), strings.ToLower(root)) || rel != "")
	}
	return !outside
}
