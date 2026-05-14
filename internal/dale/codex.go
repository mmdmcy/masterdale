package dale

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type IngestReport struct {
	HistoryEvents int `json:"history_events"`
	SessionEvents int `json:"session_events"`
	Skipped       int `json:"skipped"`
}

func IngestCodex(cfg Config, store *Store, codexDir string) (IngestReport, error) {
	if codexDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return IngestReport{}, err
		}
		codexDir = filepath.Join(home, ".codex")
	}
	var report IngestReport
	history := filepath.Join(codexDir, "history.jsonl")
	if err := ingestCodexHistory(cfg, store, history, &report); err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	sessionRoot := filepath.Join(codexDir, "sessions")
	err := filepath.WalkDir(sessionRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		added, err := ingestCodexSession(cfg, store, path)
		if err != nil {
			report.Skipped++
			return nil
		}
		if added {
			report.SessionEvents++
		} else {
			report.Skipped++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	return report, err
}

func ingestCodexHistory(cfg Config, store *Store, path string, report *IngestReport) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var row struct {
			SessionID string `json:"session_id"`
			TS        int64  `json:"ts"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil || row.Text == "" {
			report.Skipped++
			continue
		}
		ts := time.Unix(row.TS, 0).UTC().Format(time.RFC3339Nano)
		e := Event{
			ID:        DeterministicID("codex-history", row.SessionID, ts, row.Text),
			Schema:    EventSchema,
			Timestamp: ts,
			DeviceID:  cfg.DeviceID,
			Actor:     "user",
			Channel:   "codex",
			Kind:      "codex.history",
			Body: map[string]any{
				"session_id": row.SessionID,
				"text":       redact(row.Text),
			},
		}
		added, err := store.AppendIfNew(e)
		if err != nil {
			return err
		}
		if added {
			report.HistoryEvents++
		} else {
			report.Skipped++
		}
	}
	return scanner.Err()
}

func ingestCodexSession(cfg Config, store *Store, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var sessionID, cwd, firstUser string
	var messages, toolCalls int
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row["type"] == "session_meta" {
			if payload, ok := row["payload"].(map[string]any); ok {
				sessionID, _ = payload["id"].(string)
				cwd, _ = payload["cwd"].(string)
			}
		}
		if row["type"] == "response_item" {
			messages++
		}
		if row["type"] == "function_call" || row["type"] == "tool_call" {
			toolCalls++
		}
		if firstUser == "" {
			if payload, ok := row["payload"].(map[string]any); ok {
				if role, _ := payload["role"].(string); role == "user" {
					firstUser = extractText(payload)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if sessionID == "" {
		sessionID = DeterministicID(path)
	}
	e := Event{
		ID:        DeterministicID("codex-session", path, info.ModTime().String(), strconv.FormatInt(info.Size(), 10)),
		Schema:    EventSchema,
		Timestamp: info.ModTime().UTC().Format(time.RFC3339Nano),
		DeviceID:  cfg.DeviceID,
		Actor:     "system",
		Channel:   "codex",
		Kind:      "codex.session.summary",
		Body: map[string]any{
			"session_id": sessionID,
			"path":       path,
			"cwd":        cwd,
			"messages":   messages,
			"tool_calls": toolCalls,
			"summary":    Shorten(redact(firstUser), 500),
		},
	}
	return store.AppendIfNew(e)
}

func extractText(payload map[string]any) string {
	content, ok := payload["content"].([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "input_text" {
			if text, _ := m["text"].(string); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func redact(s string) string {
	replacements := []string{"api_key", "apikey", "password", "secret", "token"}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range replacements {
			if strings.Contains(lower, marker) {
				lines[i] = "[redacted sensitive line]"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
