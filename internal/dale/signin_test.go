package dale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signinRequest(method, path, body, remote string, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if strings.HasPrefix(remote, "127.0.0.1:") {
		req.Host = "127.0.0.1:7345"
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	return req
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

const tailnetBrowser = "100.64.1.2:5555"

func TestALocalRequestMakesAOneTimeSigninLink(t *testing.T) {
	server := linuxmiceTestServer(t)
	handler := server.Handler()

	// A browser on the tailnet cannot make links.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/link", "{}", tailnetBrowser))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tailnet link without sign-in: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/link", "{}", "127.0.0.1:4000"))
	if rec.Code != http.StatusOK {
		t.Fatalf("local link: %d %s", rec.Code, rec.Body)
	}
	var link struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &link)
	path := link.URL[strings.Index(link.URL, "/auth/link/"):]

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodGet, path, "", tailnetBrowser))
	session := cookieNamed(rec, sessionCookie)
	if rec.Code != http.StatusSeeOther || session == nil || rec.Header().Get("Location") != "/fleet" {
		t.Fatalf("opening the link: %d %v %q", rec.Code, session, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodGet, "/v1/session", "", tailnetBrowser, session))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"signed_in": true`) {
		t.Fatalf("signed in with the link: %d %s", rec.Code, rec.Body)
	}

	// Used once only.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodGet, path, "", tailnetBrowser))
	if cookieNamed(rec, sessionCookie) != nil || !strings.Contains(rec.Header().Get("Location"), "link=used") {
		t.Fatal("a used link must not sign in again")
	}
}

func TestAnApprovedCodeSignsInOnlyTheWaitingBrowser(t *testing.T) {
	server := linuxmiceTestServer(t)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/auth/wait", "{}", tailnetBrowser))
	if rec.Code != http.StatusOK {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body)
	}
	var wait struct{ ID, Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &wait)
	secret := cookieNamed(rec, signinWaitCookie)
	if secret == nil || len(wait.Code) != 9 {
		t.Fatalf("wait answer: %+v %v", wait, secret)
	}

	poll := func(cookies ...*http.Cookie) (string, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, signinRequest(http.MethodGet, "/auth/wait?id="+wait.ID, "", tailnetBrowser, cookies...))
		var body struct{ Status string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body.Status, rec
	}
	if status, _ := poll(secret); status != "pending" {
		t.Fatalf("before approval: %s", status)
	}

	// Another tailnet browser cannot approve; a local request (lmice) can,
	// in any spelling of the code.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/approve", `{"code":"`+wait.Code+`"}`, "100.64.9.9:1"))
	if rec.Code == http.StatusOK {
		t.Fatal("an unsigned browser approved a code")
	}
	loose := strings.ToLower(strings.ReplaceAll(wait.Code, "-", " "))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signinRequest(http.MethodPost, "/v1/session/approve", `{"code":"`+loose+`"}`, "127.0.0.1:4000"))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}

	// Knowing the id without the waiting browser's cookie is not enough.
	if status, rec := poll(); status == "approved" || cookieNamed(rec, sessionCookie) != nil {
		t.Fatal("collected without the browser's cookie")
	}
	status, rec := poll(secret)
	if status != "approved" || cookieNamed(rec, sessionCookie) == nil {
		t.Fatalf("after approval: %s", status)
	}
	if status, _ := poll(secret); status != "gone" {
		t.Fatalf("collected twice: %s", status)
	}
}

func TestTheDeviceTokenCannotMakeSigninLinks(t *testing.T) {
	server := linuxmiceTestServer(t)
	server.token = newTokenSource("device-token")
	req := signinRequest(http.MethodPost, "/v1/session/link", "{}", tailnetBrowser)
	req.Header.Set("Authorization", "Bearer device-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("token link: %d", rec.Code)
	}
}

func TestCodesAreReadable(t *testing.T) {
	for i := 0; i < 50; i++ {
		code, err := newApprovalCode()
		if err != nil || normalCode(code) != code || strings.ContainsAny(code, "01OIL") {
			t.Fatalf("code %q %v", code, err)
		}
	}
	if normalCode("abc") != "" {
		t.Fatal("short codes are not codes")
	}
	if shortBrowser("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1") != "Safari on iPhone" {
		t.Fatal(shortBrowser("iPhone Safari"))
	}
}
