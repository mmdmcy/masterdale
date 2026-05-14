package dale

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmdmcy/masterdale/internal/models"
)

type ModelConfig struct {
	Strategy   string   `json:"strategy,omitempty"`
	Primary    string   `json:"primary,omitempty"`
	Fast       string   `json:"fast,omitempty"`
	Text       string   `json:"text"`
	Context    string   `json:"context,omitempty"`
	Reasoning  string   `json:"reasoning,omitempty"`
	Structured string   `json:"structured,omitempty"`
	Vision     string   `json:"vision"`
	Fallbacks  []string `json:"fallbacks"`
}

type Config struct {
	DataDir     string      `json:"data_dir"`
	DeviceID    string      `json:"device_id"`
	SecretHex   string      `json:"secret_hex"`
	AccessToken string      `json:"access_token"`
	Listen      string      `json:"listen"`
	OllamaURL   string      `json:"ollama_url"`
	Models      ModelConfig `json:"models"`
	SafeRoots   []string    `json:"safe_roots"`
}

func DefaultDataDir() string {
	if v := os.Getenv("DALE_DATA_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".learndale"
	}
	return filepath.Join(home, ".local", "share", "learndale")
}

func DefaultConfig() Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown-device"
	}
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:     DefaultDataDir(),
		DeviceID:    hostname,
		SecretHex:   randomHex(32),
		AccessToken: randomHex(32),
		Listen:      "127.0.0.1:7345",
		OllamaURL:   "http://127.0.0.1:11434",
		Models: ModelConfig{
			Strategy:   models.StrategyPrimary,
			Primary:    models.PrimaryDefault,
			Fast:       models.FastDefault,
			Text:       models.TextDefault,
			Context:    models.ContextDefault,
			Reasoning:  models.ReasoningDefault,
			Structured: models.StructuredDefault,
			Vision:     models.VisionDefault,
			Fallbacks:  models.Fallbacks(),
		},
		SafeRoots: defaultSafeRoots(home),
	}
}

func defaultSafeRoots(home string) []string {
	if roots := safeRootsFromEnv(); len(roots) > 0 {
		return roots
	}
	githubRoot := filepath.Join(home, "Documents", "github")
	if info, err := os.Stat(githubRoot); err == nil && info.IsDir() {
		return []string{githubRoot}
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return []string{cwd}
	}
	if home != "" {
		return []string{home}
	}
	return []string{"."}
}

func safeRootsFromEnv() []string {
	roots := os.Getenv("DALE_SAFE_ROOTS")
	if roots == "" {
		return nil
	}
	var out []string
	for _, root := range filepath.SplitList(roots) {
		if strings.TrimSpace(root) != "" {
			out = append(out, root)
		}
	}
	return out
}

func LoadOrCreateConfig(dataDir string) (Config, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Config{}, err
	}
	path := filepath.Join(dataDir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := DefaultConfig()
		cfg.DataDir = dataDir
		return cfg, SaveConfig(cfg)
	}
	if err != nil {
		return Config{}, err
	}
	oldModelShape := !bytes.Contains(b, []byte(`"fast"`)) &&
		!bytes.Contains(b, []byte(`"primary"`)) &&
		!bytes.Contains(b, []byte(`"strategy"`)) &&
		!bytes.Contains(b, []byte(`"context"`)) &&
		!bytes.Contains(b, []byte(`"reasoning"`)) &&
		!bytes.Contains(b, []byte(`"structured"`))
	missingModelFields := !bytes.Contains(b, []byte(`"strategy"`)) ||
		!bytes.Contains(b, []byte(`"primary"`)) ||
		!bytes.Contains(b, []byte(`"context"`)) ||
		!bytes.Contains(b, []byte(`"reasoning"`)) ||
		!bytes.Contains(b, []byte(`"structured"`))
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = dataDir
	}
	if cfg.SecretHex == "" {
		cfg.SecretHex = randomHex(32)
	}
	if cfg.AccessToken == "" {
		cfg.AccessToken = randomHex(32)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:7345"
	}
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://127.0.0.1:11434"
	}
	changed := applyModelDefaults(&cfg.Models, oldModelShape) || missingModelFields
	if changed {
		_ = SaveConfig(cfg)
	}
	if token := os.Getenv("DALE_TOKEN"); token != "" {
		cfg.AccessToken = token
	}
	if strategy := os.Getenv("DALE_MODEL_STRATEGY"); strategy != "" {
		cfg.Models.Strategy = strategy
	}
	if primary := firstModel(os.Getenv("DALE_MODEL"), os.Getenv("DALE_PRIMARY_MODEL")); primary != "" {
		cfg.Models.Primary = primary
	}
	if roots := safeRootsFromEnv(); len(roots) > 0 {
		cfg.SafeRoots = roots
	}
	return cfg, nil
}

func applyModelDefaults(modelCfg *ModelConfig, oldModelShape bool) bool {
	changed := false
	if modelCfg.Strategy == "" {
		modelCfg.Strategy = models.StrategyPrimary
		changed = true
	}
	if modelCfg.Primary == "" {
		modelCfg.Primary = models.PrimaryDefault
		changed = true
	}
	if modelCfg.Fast == "" {
		modelCfg.Fast = models.FastDefault
		changed = true
	}
	if modelCfg.Text == "" {
		modelCfg.Text = models.TextDefault
		changed = true
	}
	if modelCfg.Context == "" {
		modelCfg.Context = models.ContextDefault
		changed = true
	}
	if oldModelShape && modelCfg.Text == models.ReasoningDefault {
		modelCfg.Text = models.TextDefault
		modelCfg.Reasoning = models.ReasoningDefault
		changed = true
	}
	if modelCfg.Reasoning == "" {
		modelCfg.Reasoning = models.ReasoningDefault
		changed = true
	}
	if modelCfg.Structured == "" {
		modelCfg.Structured = models.StructuredDefault
		changed = true
	}
	if modelCfg.Vision == "" {
		modelCfg.Vision = models.VisionDefault
		changed = true
	}
	if len(modelCfg.Fallbacks) == 0 || hasFallback(modelCfg.Fallbacks, modelCfg.Text) || hasFallback(modelCfg.Fallbacks, modelCfg.Structured) || hasFallback(modelCfg.Fallbacks, modelCfg.Reasoning) {
		modelCfg.Fallbacks = models.Fallbacks()
		changed = true
	}
	return changed
}

func (m ModelConfig) ForRole(role string) string {
	switch role {
	case models.RoleFast:
		return firstModel(m.Fast, models.FastDefault)
	case models.RoleReasoning:
		return firstModel(m.Reasoning, models.ReasoningDefault)
	case models.RoleVision:
		return firstModel(m.Vision, models.VisionDefault)
	}
	if !strings.EqualFold(m.Strategy, models.StrategyRole) {
		return firstModel(m.Primary, models.PrimaryDefault)
	}
	switch role {
	case models.RoleStructured:
		return firstModel(m.Structured, models.StructuredDefault)
	case models.RoleContext:
		return firstModel(m.Context, models.ContextDefault)
	default:
		return firstModel(m.Text, models.TextDefault)
	}
}

func (m ModelConfig) RoleModel(role string) string {
	switch role {
	case models.RoleFast:
		return firstModel(m.Fast, models.FastDefault)
	case models.RoleReasoning:
		return firstModel(m.Reasoning, models.ReasoningDefault)
	case models.RoleStructured:
		return firstModel(m.Structured, models.StructuredDefault)
	case models.RoleContext:
		return firstModel(m.Context, models.ContextDefault)
	case models.RoleVision:
		return firstModel(m.Vision, models.VisionDefault)
	default:
		return firstModel(m.Text, models.TextDefault)
	}
}

func firstModel(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func hasFallback(fallbacks []string, value string) bool {
	for _, fallback := range fallbacks {
		if fallback == value {
			return true
		}
	}
	return false
}

func SaveConfig(cfg Config) error {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.DataDir, "config.json"), b, 0o600)
}

func (c Config) Secret() []byte {
	b, err := hex.DecodeString(c.SecretHex)
	if err != nil {
		return nil
	}
	return b
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
