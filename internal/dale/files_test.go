package dale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileOperationsStayInsideSafeRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello remote files\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DALE_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ListFiles([]string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "note.txt" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if _, err := ReadFileSafe([]string{root}, ".env", 0); err == nil {
		t.Fatal("expected .env read to be rejected")
	}
	read, err := ReadFileSafe([]string{root}, "note.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "hello remote files\n" {
		t.Fatalf("unexpected content: %#v", read)
	}
	search := SearchFiles([]string{root}, "", "remote", 100, 10)
	if len(search.Matches) != 1 {
		t.Fatalf("expected match: %#v", search)
	}
	if _, err := ReadFileSafe([]string{root}, filepath.Join(root, "..", "outside.txt"), 0); err == nil {
		t.Fatal("expected outside path to be rejected")
	}
}
