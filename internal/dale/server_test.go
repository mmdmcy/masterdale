package dale

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d %s", w.Code, w.Body.String())
	}
	mcp := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcp))
	req.RemoteAddr = "127.0.0.1:12345"
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected mcp status: %d", w.Code)
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
