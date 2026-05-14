package dale

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanNPMFindsPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"left-pad":"1.3.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ScanNPM([]string{root}, []string{"left-pad"}, 100)
	if result.Files != 1 {
		t.Fatalf("unexpected file count: %#v", result)
	}
	if len(result.Matches) != 1 || result.Matches[0].Package != "left-pad" {
		t.Fatalf("unexpected matches: %#v", result)
	}
}
