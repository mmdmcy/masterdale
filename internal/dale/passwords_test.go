package dale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVaultLmice answers `vault --format json` with two logins and a note,
// `vault reveal SPEC` with the spec itself, and records every call.
func fakeVaultLmice(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	fake := filepath.Join(dir, "lmice")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + args + `
if [ "$1" = vault ] && [ "$2" = reveal ]; then printf 'value-of-%s\n' "$3"; exit 0; fi
if [ "$1" = vault ] && [ "$2" = --format ]; then
  printf '{"entries":[{"vault":"personal","name":"github","kind":"login","user":"rei","domains":["github.com"],"has_totp":true},{"vault":"work","name":"intranet","kind":"login","user":"r","domains":["intra.example.nl"]},{"vault":"personal","name":"diary","kind":"note","domains":["github.com"]}]}'
  exit 0
fi
cat >/dev/null
printf '{"ok":true}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DALE_LMICE", fake)
	t.Setenv("DALE_LINUXMICE_ACTIONS", "1")
	t.Setenv("DALE_LINUXMICE_RUNNER", "direct")
	return args
}

func extensionRequest(method, path, body, token string) *http.Request {
	req := signinRequest(method, path, body, tailnetBrowser)
	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// pairExtension runs the whole pairing: the extension asks, a device you
// own allows the code, the extension collects its token.
func pairExtension(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/pair", `{"browser":"Brave on Linux"}`, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}
	var wait struct{ ID, Code, Secret string }
	_ = json.Unmarshal(rec.Body.Bytes(), &wait)
	collect := "/v1/passwords/pair?id=" + wait.ID + "&secret=" + wait.Secret

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, collect, "", ""))
	if !strings.Contains(rec.Body.String(), `"pending"`) {
		t.Fatalf("before approval: %s", rec.Body)
	}

	// The browser's sign-in collection does not hand out an extension wait.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/approve", `{"code":"`+wait.Code+`"}`, "127.0.0.1:4000"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "LinuxMice Passwords in Brave on Linux") {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, collect, "", ""))
	var got struct{ Status, Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != "approved" || got.Token == "" {
		t.Fatalf("collect: %s", rec.Body)
	}
	return got.Token
}

func TestPasswordsExtensionPairsWithTheSigninCode(t *testing.T) {
	fakeVaultLmice(t)
	server := linuxmiceTestServer(t)
	handler := server.Handler()
	token := pairExtension(t, handler)

	// It is listed with the signed-in browsers, so Rei can end it.
	found := false
	for _, b := range server.sessions.list(httptest.NewRequest(http.MethodGet, "/", nil)) {
		if b.How == extensionHow && b.Browser == "LinuxMice Passwords in Brave on Linux" {
			found = true
		}
	}
	if !found {
		t.Fatal("the extension's session is not on the Sign-in page")
	}

	// Its token is not a browser session: as a cookie it opens nothing.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodGet, "/v1/linuxmice/health", "", tailnetBrowser, &http.Cookie{Name: sessionCookie, Value: token}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("extension token as a cookie: %d", rec.Code)
	}
}

func TestPasswordsNeedTheExtensionsBearerToken(t *testing.T) {
	fakeVaultLmice(t)
	t.Setenv("DALE_TOKEN", "shared-device-token")
	server := linuxmiceTestServer(t)
	handler := server.Handler()

	browser, err := server.sessions.issue(deviceSubject, "approved from a device", "Firefox/140 Linux")
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]*http.Request{
		"no token":        extensionRequest(http.MethodGet, "/v1/passwords/logins?site=github.com", "", ""),
		"device token":    extensionRequest(http.MethodGet, "/v1/passwords/logins?site=github.com", "", "shared-device-token"),
		"browser cookie":  signinRequest(http.MethodGet, "/v1/passwords/logins?site=github.com", "", tailnetBrowser, &http.Cookie{Name: sessionCookie, Value: browser}),
		"browser session": extensionRequest(http.MethodGet, "/v1/passwords/logins?site=github.com", "", browser),
		"local request":   signinRequest(http.MethodPost, "/v1/passwords/reveal", `{"vault":"personal","name":"github"}`, "127.0.0.1:4000"),
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestPasswordsListRevealSaveAndSignOut(t *testing.T) {
	argsFile := fakeVaultLmice(t)
	server := linuxmiceTestServer(t)
	handler := server.Handler()
	token := pairExtension(t, handler)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, "/v1/passwords/logins?site=www.github.com", "", token))
	var list struct {
		Logins []passwordLogin `json:"logins"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Logins) != 1 || list.Logins[0].Name != "github" || !list.Logins[0].HasTOTP {
		t.Fatalf("site logins (notes left out): %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, "/v1/passwords/logins?all=1", "", token))
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Logins) != 2 {
		t.Fatalf("all logins: %s", rec.Body)
	}

	for field, want := range map[string]string{"": "personal/github", "user": "personal/github:user", "code": "personal/github:code", "totp": "personal/github:code"} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/reveal", `{"vault":"personal","name":"github","field":"`+field+`"}`, token))
		if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"value-of-`+want+`"`) {
			t.Fatalf("reveal %q: %s", field, rec.Body)
		}
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/reveal", `{"vault":"personal","name":"github","field":"note"}`, token))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reveal note: %d", rec.Code)
	}

	// Saving never replaces an existing login.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/save", `{"vault":"personal","name":"github","user":"x","domains":["github.com"],"password":"p"}`, token))
	if rec.Code != http.StatusConflict {
		t.Fatalf("save over existing: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/save", `{"vault":"work","name":"forgejo","user":"rei","domains":["forge.example.nl"],"password":"hunter2-secret"}`, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--yes vault add work/forgejo --json --user rei --domain forge.example.nl") {
		t.Fatalf("save arguments: %s", args)
	}
	if strings.Contains(string(args), "hunter2") {
		t.Fatal("the password reached the command line")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, "/v1/passwords/generate?length=30", "", token))
	var generated struct{ Value string }
	_ = json.Unmarshal(rec.Body.Bytes(), &generated)
	if len(generated.Value) != 30 {
		t.Fatalf("generate: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodPost, "/v1/passwords/signout", `{}`, token))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, extensionRequest(http.MethodGet, "/v1/passwords/logins?all=1", "", token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after sign-out: %d", rec.Code)
	}
}

func TestSiteMatchesTheDomainAndBelowIt(t *testing.T) {
	for host, want := range map[string]bool{
		"github.com": true, "www.github.com": true, "gist.github.com": true,
		"evilgithub.com": false, "github.com.evil.nl": false, "": false,
	} {
		if siteMatches(host, []string{"github.com"}) != want {
			t.Fatalf("%q", host)
		}
	}
}

func TestGeneratedPasswordsMixEveryKind(t *testing.T) {
	for _, length := range []int{0, 4, 16, 500} {
		value, err := generatePassword(length)
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]int{0: 24, 4: 12, 16: 16, 500: 128}[length]
		if len(value) != want || strings.ContainsAny(value, "0O1lI") {
			t.Fatalf("%d: %q", length, value)
		}
	}
}
