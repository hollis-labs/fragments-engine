package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

const testAPIToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func securityServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := tokenfile.Write(path, testAPIToken); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(filepath.Join(t.TempDir(), "missing.yaml")).WithSecurity(Security{TokenFile: path, BrowserOrigins: []string{"https://fragments.example"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func securityRequest(h http.Handler, method, path, bearer, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8091"+path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSecurityProtectsEveryAPIFamily(t *testing.T) {
	s, _ := securityServer(t)
	h := s.Handler()
	for _, path := range []string{"transcripts/status", "reader/items", "media/variants/x/content", "captures", "intake", "config", "queue/purge", "fragments", "unknown"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, token := range []string{"", "wrong"} {
				r := securityRequest(h, method, "/v1/"+path, token, "", nil)
				if r.Code != http.StatusUnauthorized {
					t.Errorf("%s %s: got %d", method, path, r.Code)
				}
				if r.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Error("missing bearer challenge")
				}
			}
		}
	}
	for _, path := range []string{"/healthz", "/v1/auth/status", "/sysop/"} {
		r := securityRequest(h, http.MethodGet, path, "", "", nil)
		if r.Code == http.StatusUnauthorized {
			t.Errorf("public shell/health %s needs auth", path)
		}
	}
	// Valid auth reaches an existing config-free endpoint.
	if r := securityRequest(h, http.MethodGet, "/v1/capabilities", testAPIToken, "", nil); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestSecurityChromeExtensionOriginRequiresExplicitAdmissionAndBearer(t *testing.T) {
	s, tokenPath := securityServer(t)
	origin := "chrome-extension://" + strings.Repeat("a", 32)
	otherOrigin := "chrome-extension://" + strings.Repeat("b", 32)
	if r := securityRequest(s.Handler(), "GET", "/v1/capabilities", testAPIToken, origin, nil); r.Code != 403 {
		t.Fatalf("unlisted extension origin: got %d, want 403", r.Code)
	}
	_, err := s.WithSecurity(Security{TokenFile: tokenPath, BrowserOrigins: []string{origin, "https://fragments.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST", "PUT"} {
		for _, token := range []string{"", "wrong"} {
			if r := securityRequest(s.Handler(), method, "/v1/captures", token, origin, nil); r.Code != 401 {
				t.Errorf("%s with missing/invalid bearer: got %d, want 401", method, r.Code)
			}
		}
	}
	// Exercise the guard independently of capture request-body validation.
	protected := s.secureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for method, path := range map[string]string{"GET": "/v1/captures/example", "POST": "/v1/captures", "PUT": "/v1/captures/example/assets/variant/content"} {
		if r := securityRequest(protected, method, path, testAPIToken, origin, nil); r.Code != 204 {
			t.Errorf("%s with listed extension and bearer: got %d, want 204", method, r.Code)
		}
		if r := securityRequest(protected, method, path, testAPIToken, otherOrigin, nil); r.Code != 403 {
			t.Errorf("%s with different extension: got %d, want 403", method, r.Code)
		}
	}
	if r := securityRequest(s.Handler(), "GET", "/v1/capabilities", testAPIToken, "https://fragments.example", nil); r.Code != 200 {
		t.Fatalf("existing HTTPS origin: got %d, want 200", r.Code)
	}
}

func TestSecurityRejectsMalformedChromeExtensionOrigins(t *testing.T) {
	_, tokenPath := securityServer(t)
	id := strings.Repeat("a", 32)
	for _, origin := range []string{
		"chrome-extension://*", "chrome-extension://short", "chrome-extension://" + strings.Repeat("q", 32),
		"chrome-extension://" + id + ":80", "chrome-extension://" + id + "/",
		"chrome-extension://" + id + "?query=value", "chrome-extension://" + id + "#fragment",
		"chrome-extension://user@" + id, "moz-extension://" + id,
	} {
		t.Run(origin, func(t *testing.T) {
			_, err := NewServer("").WithSecurity(Security{TokenFile: tokenPath, BrowserOrigins: []string{origin}})
			if err == nil {
				t.Fatal("accepted malformed or unsupported extension origin")
			}
		})
	}
}

func TestSecurityInvalidTokenFileFailsClosedAndRotationRevokes(t *testing.T) {
	s, path := securityServer(t)
	h := s.Handler()
	if err := tokenfile.Write(path, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", testAPIToken, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", strings.Repeat("b", 64), "", nil); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", strings.Repeat("b", 64), "", nil); r.Code != 503 {
		t.Fatal(r.Code)
	}
	if _, err := NewServer("").WithSecurity(Security{TokenFile: path}); err == nil {
		t.Fatal("accepted unsafe token file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", testAPIToken, "", nil); r.Code != 503 {
		t.Fatal(r.Code)
	}
}

func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	r := securityRequest(h, "POST", "/v1/auth/session", testAPIToken, "https://fragments.example", nil)
	if r.Code != 204 {
		t.Fatal(r.Code, r.Body.String())
	}
	cookies := r.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/v1/" || c.Value == testAPIToken {
		t.Fatalf("unsafe cookie attributes")
	}
	return c
}

func TestBrowserSessionMediaCSRFAndLogout(t *testing.T) {
	s, _ := securityServer(t)
	h := s.Handler()
	cookie := login(t, h)
	if r := securityRequest(h, "GET", "/v1/capabilities", "", "", cookie); r.Code != 200 {
		t.Fatal(r.Code)
	}
	// Media uses the same cookie gate, with no credential in its URL. A missing
	// media/config resource may fail after auth, but must not return auth's 401.
	if r := securityRequest(h, "GET", "/v1/media/variants/missing/content", "", "", cookie); r.Code == 401 {
		t.Fatal("media cookie rejected")
	}
	for _, origin := range []string{"", "https://evil.example"} {
		if r := securityRequest(h, "DELETE", "/v1/auth/session", "", origin, cookie); r.Code != 403 {
			t.Fatal("cookie CSRF admitted", r.Code)
		}
	}
	if r := securityRequest(h, "POST", "/v1/auth/session", "", "https://fragments.example", cookie); r.Code != 401 {
		t.Fatal("cookie refreshed session without token", r.Code)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", "wrong", "", cookie); r.Code != 401 {
		t.Fatal("cookie rescued bad bearer", r.Code)
	}
	if r := securityRequest(h, "DELETE", "/v1/auth/session", "", "https://fragments.example", cookie); r.Code != 204 {
		t.Fatal(r.Code)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", "", "", cookie); r.Code != 401 {
		t.Fatal("logout did not revoke session", r.Code)
	}
}

func TestBrowserSessionExpiryAndRotation(t *testing.T) {
	s, path := securityServer(t)
	h := s.Handler()
	cookie := login(t, h)
	for key, session := range s.security.sessions {
		session.expires = time.Now().Add(-time.Minute)
		s.security.sessions[key] = session
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", "", "", cookie); r.Code != 401 {
		t.Fatal("expired session admitted", r.Code)
	}
	cookie = login(t, h)
	if err := tokenfile.Write(path, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if r := securityRequest(h, "GET", "/v1/capabilities", "", "", cookie); r.Code != 401 {
		t.Fatal("rotation did not revoke session", r.Code)
	}
}

func TestProxyOriginAndExistingTranscriptGuards(t *testing.T) {
	s, _ := securityServer(t)
	h := s.Handler()
	if r := securityRequest(h, "POST", "/v1/auth/session", testAPIToken, "https://evil.example", nil); r.Code != 403 {
		t.Fatal("foreign origin admitted", r.Code)
	}
	// Proxy rewrites Host for private routes: configured public Origin remains
	// valid, while the feature-off guard still returns 404 after authentication.
	if r := securityRequest(h, "GET", "/v1/transcripts/status", testAPIToken, "https://fragments.example", nil); r.Code != 404 {
		t.Fatal(r.Code)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8091/v1/capabilities", nil)
	r.Header.Set("Authorization", "Bearer "+testAPIToken)
	r.Header.Add("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("duplicate auth accepted", w.Code)
	}
	if err := NewServer("").validateListen("0.0.0.0:8091"); err == nil {
		t.Fatal("unauthenticated remote bind allowed")
	}
	if _, err := NewServer("").WithSecurity(Security{TokenFile: s.security.watch.Path(), BrowserOrigins: []string{"http://remote.example"}}); err == nil {
		t.Fatal("insecure origin accepted")
	}
}

func TestAuthenticatedTranscriptProxyKeepsLoopbackBoundary(t *testing.T) {
	s, _ := securityServer(t)
	s.cfgPath = writeTranscriptAPIConfig(t, true)
	h := s.Handler()
	for _, tc := range []struct {
		peer, host string
		status     int
	}{
		{"127.0.0.1:1234", "127.0.0.1:8091", 200},
		{"203.0.113.1:1234", "127.0.0.1:8091", 403},
		{"127.0.0.1:1234", "fragments.example", 403},
	} {
		r := httptest.NewRequest("GET", "/v1/transcripts/status", nil)
		r.RemoteAddr, r.Host = tc.peer, tc.host
		r.Header.Set("Authorization", "Bearer "+testAPIToken)
		r.Header.Set("Origin", "https://fragments.example")
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("peer=%s host=%s: got %d", tc.peer, tc.host, w.Code)
		}
		if w.Code == 200 && !strings.Contains(w.Body.String(), `"auth":"bearer"`) {
			t.Fatal("status does not report auth")
		}
	}
	if err := s.validateListen("0.0.0.0:8091"); err == nil {
		t.Fatal("auth removed transcript listen boundary")
	}
}

func TestBrowserSessionRefusesCrossSiteReads(t *testing.T) {
	s, _ := securityServer(t)
	h := s.Handler()
	cookie := login(t, h)
	for _, site := range []string{"cross-site", "same-site"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8091/v1/capabilities", nil)
		r.AddCookie(cookie)
		r.Header.Set("Sec-Fetch-Site", site)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s cookie read admitted", site)
		}
	}
}
