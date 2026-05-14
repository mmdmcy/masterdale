package dale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIngestCodex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions", "2026", "05", "09"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(`{"session_id":"s1","ts":1778320650,"text":"hello codex"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := `{"type":"session_meta","payload":{"id":"s1","cwd":"/tmp"}}
{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"do work"}]}}
`
	if err := os.WriteFile(filepath.Join(root, "sessions", "2026", "05", "09", "rollout.jsonl"), []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DeviceID = "test"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := IngestCodex(cfg, store, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.HistoryEvents != 1 || report.SessionEvents != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
}
