package dale

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerEventsAndMCP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DeviceID = "test-device"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, store)
	body := []byte(`{"actor":"user","channel":"chat","kind":"message.created","body":{"text":"hello"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7345"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d %s", w.Code, w.Body.String())
	}
	mcp := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcp))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7345"
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected mcp status: %d", w.Code)
	}
}

func TestServerListenAndServeContextStopsOnCancel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Listen = "127.0.0.1:0"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewServer(cfg, store).ListenAndServeContext(ctx)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean shutdown after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

func TestServerDashboardRoutes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.SafeRoots = []string{t.TempDir()}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, store)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7345"
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected dashboard status: %d %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("Masterdale Dashboard")) {
		t.Fatalf("dashboard did not render expected title")
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/dashboard", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected dashboard data status: %d %s", w.Code, w.Body.String())
	}
	var overview DashboardOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Node.DeviceID == "" {
		t.Fatalf("expected node metadata: %#v", overview.Node)
	}
}

func TestServerRemoteNetworkGuard(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AccessToken = "shared-test-token"
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, store)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.168.1.20:12345"
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected private LAN health to pass: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected tailnet health to pass: %d %s", w.Code, w.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if _, ok := health["device_id"]; ok {
		t.Fatalf("health should not expose device id: %#v", health)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected public internet remote to be blocked: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/node/report", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected tailnet API request without token to require auth: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected dashboard shell to load on private network: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/fleet", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected the fleet page shell to load on private network: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/linuxmice/devices", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected fleet data to require auth remotely: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/dashboard", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected dashboard data to require auth remotely: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/node/report", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected tailnet API request with token to pass: %d %s", w.Code, w.Body.String())
	}
}

func TestServerRemoteScopeTailnet(t *testing.T) {
	t.Setenv("DALE_REMOTE_SCOPE", "tailnet")
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, store)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.168.1.20:12345"
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected private LAN to be blocked in tailnet scope: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "100.64.0.42:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected tailnet scope to allow Tailscale: %d %s", w.Code, w.Body.String())
	}
}

func TestServerRemoteScopePublic(t *testing.T) {
	t.Setenv("DALE_REMOTE_SCOPE", "public")
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(cfg, store)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected explicit public scope to pass health: %d %s", w.Code, w.Body.String())
	}
}

// signInBrowser signs a tailnet browser in through a local one-time link
// and returns its session cookie.
func signInBrowser(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/link", "{}", "127.0.0.1:4000"))
	var link struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &link)
	if rec.Code != http.StatusOK || !strings.Contains(link.URL, "/auth/link/") {
		t.Fatalf("local link: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodGet, link.URL[strings.Index(link.URL, "/auth/link/"):], "", tailnetBrowser))
	session := cookieNamed(rec, sessionCookie)
	if session == nil {
		t.Fatalf("opening the link: %d", rec.Code)
	}
	return session
}

func TestExecRefusesACookiePostFromAnotherPort(t *testing.T) {
	t.Setenv("DALE_REMOTE_EXEC", "1")
	server := linuxmiceTestServer(t)
	server.cfg.SafeRoots = []string{t.TempDir()}
	server.token = newTokenSource("device-token")
	handler := server.Handler()
	session := signInBrowser(t, handler)

	// A page on another port of the same host posts text/plain with the
	// SameSite=Lax session cookie attached: refused before anything runs.
	req := signinRequest(http.MethodPost, "/v1/exec", `{"command":"true"}`, tailnetBrowser, session)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "http://"+req.Host+":9999")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-port cookie post: %d %s", rec.Code, rec.Body)
	}

	// The fleet page's own JSON post keeps working.
	req = signinRequest(http.MethodPost, "/v1/exec", `{"command":"true"}`, tailnetBrowser, session)
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin cookie post: %d %s", rec.Code, rec.Body)
	}

	// The dale CLI: bearer token, JSON, no Origin.
	req = signinRequest(http.MethodPost, "/v1/exec", `{"command":"true"}`, tailnetBrowser)
	req.Header.Set("Authorization", "Bearer device-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer post: %d %s", rec.Code, rec.Body)
	}
}

func TestNPMScanRootStaysInsideSafeRoots(t *testing.T) {
	server := linuxmiceTestServer(t)
	server.cfg.SafeRoots = []string{t.TempDir()}
	handler := server.Handler()
	for root, want := range map[string]int{"/etc": http.StatusBadRequest, "": http.StatusOK, "sub/../": http.StatusOK} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, signinRequest(http.MethodGet, "/v1/scans/npm?package=left-pad&root="+root, "", "127.0.0.1:4000"))
		if rec.Code != want {
			t.Fatalf("root %q: %d %s", root, rec.Code, rec.Body)
		}
	}
}
