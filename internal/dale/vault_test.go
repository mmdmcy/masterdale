package dale

import (
	"net/http/httptest"
	"os"
	"path/filepath"
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
