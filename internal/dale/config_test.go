package dale

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmdmcy/masterdale/internal/models"
)

func TestDefaultConfigUsesRoleBasedLocalModels(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Models.Strategy != models.StrategyPrimary {
		t.Fatalf("model strategy = %q, want %q", cfg.Models.Strategy, models.StrategyPrimary)
	}
	if cfg.Models.Primary != models.PrimaryDefault {
		t.Fatalf("primary model = %q, want %q", cfg.Models.Primary, models.PrimaryDefault)
	}
	if cfg.Models.Fast != models.FastDefault {
		t.Fatalf("fast model = %q, want %q", cfg.Models.Fast, models.FastDefault)
	}
	if cfg.Models.Text != models.TextDefault {
		t.Fatalf("text model = %q, want %q", cfg.Models.Text, models.TextDefault)
	}
	if cfg.Models.Context != models.ContextDefault {
		t.Fatalf("context model = %q, want %q", cfg.Models.Context, models.ContextDefault)
	}
	if cfg.Models.Reasoning != models.ReasoningDefault {
		t.Fatalf("reasoning model = %q, want %q", cfg.Models.Reasoning, models.ReasoningDefault)
	}
	if cfg.Models.Structured != models.StructuredDefault {
		t.Fatalf("structured model = %q, want %q", cfg.Models.Structured, models.StructuredDefault)
	}
	if cfg.Models.Vision != models.VisionDefault {
		t.Fatalf("vision model = %q, want %q", cfg.Models.Vision, models.VisionDefault)
	}
	if got := cfg.Models.ForRole(models.RoleStructured); got != models.PrimaryDefault {
		t.Fatalf("structured role default = %q, want primary %q", got, models.PrimaryDefault)
	}
	cfg.Models.Strategy = models.StrategyRole
	if got := cfg.Models.ForRole(models.RoleStructured); got != models.StructuredDefault {
		t.Fatalf("structured role model = %q, want %q", got, models.StructuredDefault)
	}
	cfg.Models.Strategy = models.StrategyPrimary
	if got := cfg.Models.RoleModel(models.RoleStructured); got != models.StructuredDefault {
		t.Fatalf("explicit structured role model = %q, want %q", got, models.StructuredDefault)
	}
}

func TestLoadConfigMigratesOldQwenTextDefaultIntoReasoning(t *testing.T) {
	dataDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	cfg.Models.Strategy = ""
	cfg.Models.Primary = ""
	cfg.Models.Fast = ""
	cfg.Models.Text = "qwen3.5:4b"
	cfg.Models.Context = ""
	cfg.Models.Reasoning = ""
	cfg.Models.Structured = ""
	cfg.Models.Fallbacks = []string{"ministral-3:3b", "nemotron-3-nano:4b"}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOrCreateConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models.Text != models.TextDefault {
		t.Fatalf("text model = %q, want %q", loaded.Models.Text, models.TextDefault)
	}
	if loaded.Models.Primary != models.PrimaryDefault {
		t.Fatalf("primary model = %q, want %q", loaded.Models.Primary, models.PrimaryDefault)
	}
	if loaded.Models.Reasoning != models.ReasoningDefault {
		t.Fatalf("reasoning model = %q, want %q", loaded.Models.Reasoning, models.ReasoningDefault)
	}
	if loaded.Models.Context != models.ContextDefault {
		t.Fatalf("context model = %q, want %q", loaded.Models.Context, models.ContextDefault)
	}
	for _, fallback := range loaded.Models.Fallbacks {
		if fallback == models.TextDefault {
			t.Fatalf("text default should not remain in fallbacks: %#v", loaded.Models.Fallbacks)
		}
	}
}
