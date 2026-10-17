package dale

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultRefSpecAcceptsOnlyPlainNames(t *testing.T) {
	good := map[vaultRef]string{
		{Vault: "work", Name: "github"}:                        "work/github",
		{Vault: "personal", Name: "dale-token", Field: "totp"}: "personal/dale-token:totp",
	}
	for ref, want := range good {
		if got, err := ref.spec(); err != nil || got != want {
			t.Fatalf("%+v: got %q, %v", ref, got, err)
		}
	}
	for _, ref := range []vaultRef{
		{Vault: "work", Name: "../x"},
		{Vault: "Work", Name: "x"},
		{Vault: "work", Name: "-flag"},
		{Vault: "work", Name: "x", Field: "secret"},
	} {
		if _, err := ref.spec(); err == nil {
			t.Fatalf("%+v should be refused", ref)
		}
	}
}

func TestTokenSourceRereadsTheEnvFileWhenItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("OTHER=1\nDALE_TOKEN=first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DALE_ENV_FILE", path)
	source := newTokenSource("fallback")
	if got := source.current(); got != "first" {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(path, []byte("DALE_TOKEN=\"second\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	if got := source.current(); got != "second" {
		t.Fatalf("after rotation got %q", got)
	}
}

func TestOnlyThePhoneAnswerSkipsTheSession(t *testing.T) {
	if !isVaultAnswerRoute(httptest.NewRequest("POST", "/v1/vault/answer?id=x", nil)) {
		t.Fatal("POST /v1/vault/answer is the phone's button")
	}
	for _, request := range []struct{ method, path string }{
		{"GET", "/v1/vault/answer"},
		{"POST", "/v1/vault/reveal"},
		{"POST", "/v1/vault/answer/../reveal"},
	} {
		if isVaultAnswerRoute(httptest.NewRequest(request.method, request.path, nil)) {
			t.Fatalf("%s %s must need a session", request.method, request.path)
		}
	}
}

func TestVaultActionsNeedASignedInPersonEvenLocally(t *testing.T) {
	t.Setenv("DALE_LINUXMICE_ACTIONS", "1")
	s := &Server{linuxmice: &linuxmiceCache{}}
	for _, path := range []string{"/v1/vault/reveal", "/v1/vault/add", "/v1/vault/rotate", "/v1/vault/decide"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		s.handleVault(w, r)
		if w.Code != 403 {
			t.Fatalf("%s from loopback without a session: %d", path, w.Code)
		}
	}
}

func TestHTTPSJobsTakeOnlyPlainNamesPortsAndPrivateUpstreams(t *testing.T) {
	good := LinuxMiceJobRequest{Kind: "https-add", ID: "dale", A: "7443", B: "http://127.0.0.1:7345"}
	if args, changes, err := linuxmiceJobArgs(good); err != nil || !changes || args[len(args)-1] != "http://127.0.0.1:7345" {
		t.Fatalf("good request: %v %v %v", args, changes, err)
	}
	for _, bad := range []LinuxMiceJobRequest{
		{Kind: "https-add", ID: "dale", A: "80", B: "http://127.0.0.1:7345"},
		{Kind: "https-add", ID: "Dale", A: "7443", B: "http://127.0.0.1:7345"},
		{Kind: "https-add", ID: "dale", A: "7443", B: "http://example.com:80"},
		{Kind: "https-add", ID: "dale", A: "7443", B: "http://127.0.0.1:7345; rm -rf /"},
		{Kind: "https-remove", ID: "--yes"},
	} {
		if _, _, err := linuxmiceJobArgs(bad); err == nil {
			t.Fatalf("%+v should be refused", bad)
		}
	}
}

func TestFleetJobsTakeOnlyAPlainDevice(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "fleet-apply", Device: "server"})
	if err != nil || !changes || args[len(args)-1] != "server" {
		t.Fatalf("fleet-apply: %v %v %v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "fleet-apply", Device: "--as"}); err == nil {
		t.Fatal("a flag must not pass as a device")
	}
	if args, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "fleet-capture"}); err != nil || args[len(args)-1] != "--commit" {
		t.Fatalf("fleet-capture: %v %v", args, err)
	}
}

func TestModuleJobsTakeOnlyAPlainID(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "module-off", ID: "vault"})
	if err != nil || !changes || strings.Join(args, " ") != "modules off vault" {
		t.Fatalf("module-off: %v %v %v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "module-on", ID: "--yes"}); err == nil {
		t.Fatal("a flag must not pass as a module")
	}
}

func TestServiceRestartTakesOnlyPlainNames(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "service-restart", ID: "selfhost-ntfy", Device: "server"})
	if err != nil || !changes || strings.Join(args, " ") != "--yes services restart selfhost-ntfy --device server" {
		t.Fatalf("service-restart: %v %v %v", args, changes, err)
	}
	for _, bad := range []LinuxMiceJobRequest{
		{Kind: "service-restart", ID: "--all", Device: "server"},
		{Kind: "service-restart", ID: "x; reboot", Device: "server"},
		{Kind: "service-restart", ID: "ntfy", Device: "-oProxyCommand"},
	} {
		if _, _, err := linuxmiceJobArgs(bad); err == nil {
			t.Fatalf("%+v should be refused", bad)
		}
	}
}

func TestDisplayJobsTakeOnlyADevice(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "display-on", Device: "server"})
	if err != nil || !changes || strings.Join(args, " ") != "--yes display virtual on --device server" {
		t.Fatalf("display-on: %v %v %v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "display-off", Device: "-oProxyCommand=x"}); err == nil {
		t.Fatal("a flag must not pass as a device")
	}
}

func TestNetJobsTakeOnlyPlainNames(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "net-join", Device: "mini"})
	if err != nil || !changes || strings.Join(args, " ") != "--yes net join --device mini" {
		t.Fatalf("net-join: %v %v %v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "net-remove", ID: "--all"}); err == nil {
		t.Fatal("a flag must not pass as a member")
	}
}

func TestCheckoutUpdateJobTakesAnOptionalPlainDevice(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "checkouts-update"})
	if err != nil || !changes || strings.Join(args, " ") != "--yes --format json checkouts update" {
		t.Fatalf("all: %v %v %v", args, changes, err)
	}
	if _, _, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "checkouts-update", Device: "--all"}); err == nil {
		t.Fatal("a flag must not pass as a device")
	}
}

func TestT3CodePairLinkAcceptsOnlyAPairPage(t *testing.T) {
	good := []byte("note\n{\"data\":{\"handoff\":\"https://box.example.ts.net/pair#token=abc\"}}\nyes: accepted\n")
	if link, ok := t3codePairLink(good); !ok || link != "https://box.example.ts.net/pair#token=abc" {
		t.Fatalf("got %q %v", link, ok)
	}
	for _, handoff := range []string{
		"http://box.example.ts.net/pair#token=abc",
		"https://box.example.ts.net/other#token=abc",
		"https://box.example.ts.net/pair",
		"https://user@box.example.ts.net/pair#token=abc",
		"javascript:alert(1)",
		"",
	} {
		out, _ := json.Marshal(map[string]any{"data": map[string]string{"handoff": handoff}})
		if _, ok := t3codePairLink(out); ok {
			t.Fatalf("accepted %q", handoff)
		}
	}
}

func TestRemoveAndGithubJobsAreFixed(t *testing.T) {
	args, changes, err := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "remove", ID: "opencode", Device: "mini", A: "data"})
	if err != nil || !changes || strings.Join(args, " ") != "--yes --format json remove opencode --device mini --with-data" {
		t.Fatalf("got %v %v %v", args, changes, err)
	}
	for _, bad := range []LinuxMiceJobRequest{
		{Kind: "remove", ID: "-rf", Device: "mini"},
		{Kind: "remove", ID: "a b", Device: "mini"},
		{Kind: "remove", ID: "opencode", Device: "--fleet"},
		{Kind: "remove", ID: "opencode"},
	} {
		if _, _, err := linuxmiceJobArgs(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if args, _, _ := linuxmiceJobArgs(LinuxMiceJobRequest{Kind: "github-mirror"}); strings.Join(args, " ") != "--yes --format json github mirror" {
		t.Fatalf("github-mirror: %v", args)
	}
}
