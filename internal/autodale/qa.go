package autodale

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type CodexQA struct {
	HistoryItems int      `json:"history_items"`
	Recent       []string `json:"recent"`
	Risks        []string `json:"risks"`
}

func AnalyzeCodexHistory(codexDir string, limit int) (CodexQA, error) {
	if codexDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return CodexQA{}, err
		}
		codexDir = filepath.Join(home, ".codex")
	}
	f, err := os.Open(filepath.Join(codexDir, "history.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return CodexQA{Risks: []string{"no Codex history file found"}}, nil
	}
	if err != nil {
		return CodexQA{}, err
	}
	defer f.Close()
	var texts []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var row struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err == nil && row.Text != "" {
			texts = append(texts, row.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return CodexQA{}, err
	}
	start := len(texts) - limit
	if start < 0 {
		start = 0
	}
	recent := make([]string, 0, len(texts[start:]))
	var risks []string
	for _, text := range texts[start:] {
		recent = append(recent, shorten(strings.ReplaceAll(text, "\n", " "), 180))
		lower := strings.ToLower(text)
		if strings.Contains(lower, "api key") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
			risks = append(risks, "recent Codex prompt may mention sensitive credentials")
		}
		if strings.Contains(lower, "dangerously") || strings.Contains(lower, "unsafe") {
			risks = append(risks, "recent Codex prompt mentions unsafe/bypass behavior")
		}
	}
	if len(risks) == 0 {
		risks = append(risks, "no obvious risks in recent history text")
	}
	return CodexQA{HistoryItems: len(texts), Recent: recent, Risks: dedupe(risks)}, nil
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
