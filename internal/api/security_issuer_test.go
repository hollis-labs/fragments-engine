package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/store"
	"github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

type testIssuer struct {
	mu         sync.Mutex
	pub        ed25519.PublicKey
	key        ed25519.PrivateKey
	kid        string
	denied     auth.DenyListDoc
	down       bool
	credential string
	server     *httptest.Server
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i := &testIssuer{pub: pub, key: key, kid: "key1", credential: strings.Repeat("m", 64)}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.down {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+i.credential {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": i.kid, "x": base64.RawURLEncoding.EncodeToString(i.pub)}}})
		case "/auth/denylist":
			_ = json.NewEncoder(w).Encode(i.denied)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *testIssuer) token(t *testing.T, overrides map[string]any) string {
	t.Helper()
	i.mu.Lock()
	defer i.mu.Unlock()
	claims := map[string]any{"iss": "tether", "aud": "fragments-engine", "sub": "svc:basalisk", "kind": "service", "scope": "fragments-engine.capture", "exp": time.Now().Add(15 * time.Minute).Unix(), "jti": "token1"}
	for k, v := range overrides {
		claims[k] = v
	}
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": i.kid})
	payload, _ := json.Marshal(claims)
	data := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return data + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(i.key, []byte(data)))
}

func issuerServer(t *testing.T, i *testIssuer) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	owner := filepath.Join(dir, "owner")
	maintain := filepath.Join(dir, "maintain")
	if err := tokenfile.Write(owner, testAPIToken); err != nil {
		t.Fatal(err)
	}
	if err := tokenfile.Write(maintain, i.credential); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(writeCaptureAPIConfig(t)).WithSecurity(Security{TokenFile: owner, BrowserOrigins: []string{"https://fragments.example"}, Issuer: IssuerSecurity{Name: "tether", URL: i.server.URL, TokenFile: maintain}})
	if err != nil {
		t.Fatal(err)
	}
	return s, maintain
}

func meshCaptureRequest(h http.Handler, method, path, token string, raw []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8091"+path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIssuerCredentialAndScopeBoundaries(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	protected := s.secureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if !ok || !p.Verified || p.ID != "svc:basalisk" {
			t.Error("verified service principal missing")
		}
		w.WriteHeader(204)
	}))
	valid := i.token(t, nil)
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{"valid", nil, 204}, {"wrong audience", map[string]any{"aud": "basalisk"}, 401}, {"array audience", map[string]any{"aud": []string{"fragments-engine"}}, 401},
		{"wrong issuer", map[string]any{"iss": "other"}, 401}, {"expired", map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}, 401},
		{"future", map[string]any{"nbf": time.Now().Add(time.Minute).Unix()}, 401}, {"no scope", map[string]any{"scope": ""}, 403}, {"read only", map[string]any{"scope": "fragments-engine.read"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := securityRequest(protected, "POST", "/v1/captures", i.token(t, tc.claims), "", nil)
			if w.Code != tc.want {
				t.Fatalf("got%d want%d %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	// Tampered signature and wrong algorithm cannot use the static owner fallback.
	bad := strings.Split(valid, ".")
	bad[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	if w := securityRequest(protected, "POST", "/v1/captures", strings.Join(bad, "."), "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "none", "kid": "key1"})
	bad = strings.Split(valid, ".")
	bad[0] = base64.RawURLEncoding.EncodeToString(hdr)
	if w := securityRequest(protected, "POST", "/v1/captures", strings.Join(bad, "."), "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/transcripts/sessions"}, {"GET", "/v1/fragments"}, {"POST", "/v1/config/update"}, {"POST", "/v1/destinations/create"}, {"POST", "/v1/routes/delete"}, {"POST", "/v1/intake"}, {"POST", "/v1/auth/session"}, {"DELETE", "/v1/auth/session"}, {"GET", "/v1/future-route"},
	} {
		if w := securityRequest(s.Handler(), tc.method, tc.path, valid, "", nil); w.Code != 403 {
			t.Errorf("%s %s got%d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	// Even an overprivileged mesh subject named like the local owner is not that credential.
	for _, path := range []string{"/v1/transcripts/status", "/v1/auth/session"} {
		tok := i.token(t, map[string]any{"sub": "fragments-engine/owner", "scope": "fragments-engine.admin", "jti": ""})
		if w := securityRequest(s.Handler(), "GET", path, tok, "", nil); w.Code != 403 {
			t.Fatalf("mesh owner impersonation %s got%d", path, w.Code)
		}
	}
	session := securityRequest(s.Handler(), "POST", "/v1/auth/session", testAPIToken, "https://fragments.example", nil)
	if session.Code != 204 || len(session.Result().Cookies()) != 1 {
		t.Fatalf("owner session %d", session.Code)
	}
	cookie := session.Result().Cookies()[0]
	if w := securityRequest(s.Handler(), "GET", "/v1/capabilities", "invalid.jwt.value", "", cookie); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s.Handler(), "GET", "/v1/capabilities", valid, "", cookie); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s.Handler(), "GET", "/v1/capabilities", "", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(protected, "POST", "/v1/captures", valid, "https://hostile.example", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s.Handler(), "GET", "/v1/future-route", testAPIToken, "", nil); w.Code != 403 {
		t.Fatal("unmapped owner route did not fail closed", w.Code)
	}
}

func TestIssuerRevocationRotationAndUnavailable(t *testing.T) {
	i := newTestIssuer(t)
	s, maintain := issuerServer(t, i)
	h := s.secureHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	tok := i.token(t, nil)
	expect := func(token string, want int) {
		t.Helper()
		if w := securityRequest(h, "POST", "/v1/captures", token, "", nil); w.Code != want {
			t.Fatalf("got%d want%d %s", w.Code, want, w.Body.String())
		}
	}
	i.mu.Lock()
	i.down = true
	i.mu.Unlock()
	expect(tok, 503)
	expect(testAPIToken, 204)
	i.mu.Lock()
	i.down = false
	i.mu.Unlock()
	expect(tok, 204)
	for _, kind := range []string{"jti", "sub", "sid"} {
		claims := map[string]any{"sid": "tether-session"}
		active := i.token(t, claims)
		i.mu.Lock()
		switch kind {
		case "jti":
			i.denied = auth.DenyListDoc{JTI: []string{"token1"}}
		case "sub":
			i.denied = auth.DenyListDoc{Sub: []string{"svc:basalisk"}}
		case "sid":
			i.denied = auth.DenyListDoc{SID: []string{"tether-session"}}
		}
		i.mu.Unlock()
		s.security.issuer.mu.Lock()
		s.security.issuer.lastPoll = time.Time{}
		s.security.issuer.mu.Unlock()
		expect(active, 401)
	}
	i.mu.Lock()
	i.denied = auth.DenyListDoc{}
	i.mu.Unlock()
	s.security.issuer.lastPoll = time.Time{}
	expect(tok, 204)
	// Replacing the key set removes the old key and admits the new one.
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	i.pub = pub
	i.key = key
	i.kid = "key2"
	i.mu.Unlock()
	if err := s.security.issuer.jwt.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	expect(tok, 401)
	expect(i.token(t, nil), 204)
	// Polling uses the current private maintain token, without restarting FE.
	i.mu.Lock()
	i.credential = strings.Repeat("n", 64)
	i.mu.Unlock()
	if err := tokenfile.Write(maintain, strings.Repeat("n", 64)); err != nil {
		t.Fatal(err)
	}
	s.security.issuer.lastPoll = time.Time{}
	expect(i.token(t, nil), 204)
	if err := os.Chmod(maintain, 0644); err != nil {
		t.Fatal(err)
	}
	s.security.issuer.lastPoll = time.Time{}
	expect(i.token(t, nil), 503)
	expect(testAPIToken, 204)
}

func TestMeshCaptureIdentityReplayAndOwnership(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	h := s.Handler()
	token := i.token(t, nil)
	raw := readCaptureAPIFixture(t, "valid-capture-youtube.json")
	var env capturecontract.CaptureEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.PrincipalID = "svc:basalisk"
	env.Extensions = capturecontract.ProviderExtensions{"provider.basalisk": {"principal_id": "chrispian", "from": "owner", "session_id": "activity-session", "project_id": "asserted-project"}}
	raw, _ = json.Marshal(env)
	request := func(method, path, tok string, b []byte, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := meshCaptureRequest(h, method, path, tok, b)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	spoof := env
	spoof.PrincipalID = "chrispian"
	spoofRaw, _ := json.Marshal(spoof)
	denied := request("POST", "/v1/captures", token, spoofRaw, 403)
	if denied.Header().Get("Content-Type") != "application/json" || !strings.Contains(denied.Body.String(), `"code":"forbidden"`) {
		t.Fatal("principal denial lost auth error contract")
	}
	from := map[string]any{}
	_ = json.Unmarshal(raw, &from)
	from["from"] = "chrispian"
	fromRaw, _ := json.Marshal(from)
	request("POST", "/v1/captures", token, fromRaw, 400)
	first := request("POST", "/v1/captures", token, raw, 201)
	var accepted capturecontract.CaptureManifestResponse
	_ = json.Unmarshal(first.Body.Bytes(), &accepted)
	if accepted.FragmentID == "" || accepted.FragmentRevisionID == "" || accepted.CaptureAttemptID == "" {
		t.Fatal("missing confirmed identifiers")
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := instance.Captures.GetStatus(context.Background(), env.CaptureID)
	if err != nil || a.CaptureID != env.CaptureID {
		t.Fatal(err)
	}
	var subject, actor, principal string
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.DB.QueryRow("SELECT authenticated_subject,actor_id,principal_id FROM capture_attempts WHERE capture_id=?", env.CaptureID).Scan(&subject, &actor, &principal); err != nil {
		t.Fatal(err)
	}
	instance.Close()
	if subject != "svc:basalisk" || actor != subject || principal != subject {
		t.Fatalf("unverified attribution %q %q %q", subject, actor, principal)
	}
	replay := request("POST", "/v1/captures", token, raw, 200)
	var result capturecontract.CaptureManifestResponse
	_ = json.Unmarshal(replay.Body.Bytes(), &result)
	if !result.IdempotentReplay {
		t.Fatal("missing replay marker")
	}
	result.IdempotentReplay = false
	firstCanonical, _ := json.Marshal(accepted)
	replayCanonical, _ := json.Marshal(result)
	if !bytes.Equal(firstCanonical, replayCanonical) {
		t.Fatal("replay changed immutable receipt")
	}
	changed := env
	changed.Document.Title = "edited"
	changedRaw, _ := json.Marshal(changed)
	request("POST", "/v1/captures", token, changedRaw, 409)
	other := i.token(t, map[string]any{"sub": "svc:other", "jti": "other"})
	otherEnv := env
	otherEnv.PrincipalID = "svc:other"
	otherRaw, _ := json.Marshal(otherEnv)
	conflict := request("POST", "/v1/captures", other, otherRaw, 409)
	if strings.Contains(conflict.Body.String(), accepted.FragmentID) {
		t.Fatal("foreign conflict leaked receipt")
	}
	base := "/v1/captures/" + env.CaptureID
	request("GET", base, other, nil, 404)
	request("POST", base+"/complete", other, []byte(`{}`), 404)
	upload := httptest.NewRequest("PUT", "http://127.0.0.1:8091"+base+"/assets/poster:maxresdefault/content", strings.NewReader("foreign"))
	upload.Header.Set("Authorization", "Bearer "+other)
	upload.Header.Set("Content-Type", "image/png")
	upload.Header.Set("Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(make([]byte, 32))+":")
	uploadResult := httptest.NewRecorder()
	h.ServeHTTP(uploadResult, upload)
	if uploadResult.Code != 404 {
		t.Fatalf("foreign upload got%d %s", uploadResult.Code, uploadResult.Body.String())
	}
	request("GET", base, token, nil, 200)
	content := []byte("disposable service asset")
	digest := sha256.Sum256(content)
	for range 2 {
		upload := httptest.NewRequest("PUT", "http://127.0.0.1:8091"+base+"/assets/poster:maxresdefault/content", bytes.NewReader(content))
		upload.Header.Set("Authorization", "Bearer "+token)
		upload.Header.Set("Content-Type", "image/png")
		upload.Header.Set("Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, upload)
		if w.Code != 204 {
			t.Fatalf("own asset upload/retry got%d %s", w.Code, w.Body.String())
		}
	}

	completion := []byte(`{"schema_version":"fe.capture.completion.v1","idempotency_key":"service-complete","assets":[],"warnings":[]}`)
	done := request("POST", base+"/complete", token, completion, 200)
	again := request("POST", base+"/complete", token, completion, 200)
	if done.Body.String() != again.Body.String() {
		t.Fatal("completion replay changed receipt")
	}
	request("POST", base+"/complete", token, bytes.Replace(completion, []byte(`"warnings":[]`), []byte(`"warnings":[{"code":"changed","message":"changed"}]`), 1), 409)
	request("POST", "/v1/captures", token, raw, 200)
	// A legacy capture with the same claimed principal is not a verified service capture.
	legacy := env
	legacy.CaptureID = "legacy-capture"
	legacy.Annotations = []capturecontract.CaptureAnnotation{}
	legacyRaw, _ := json.Marshal(legacy)
	request("POST", "/v1/captures", testAPIToken, legacyRaw, 201)
	request("GET", "/v1/captures/legacy-capture", token, nil, 404)
	request("POST", "/v1/captures", token, legacyRaw, 409)
	// Re-capturing the same source cannot expose the earlier owner's private tags.
	legacy.CaptureID = "private-owner-capture"
	legacy.Tags = []string{"owner-private-tag"}
	legacyRaw, _ = json.Marshal(legacy)
	request("POST", "/v1/captures", testAPIToken, legacyRaw, 201)
	env.CaptureID = "new-service-capture"
	env.Annotations = []capturecontract.CaptureAnnotation{}
	freshRaw, _ := json.Marshal(env)
	response := request("POST", "/v1/captures", token, freshRaw, 201)
	if strings.Contains(response.Body.String(), "owner-private-tag") {
		t.Fatal("receipt exposed another capture's tag")
	}
}

func TestIssuerConfigurationCannotDisableOwnerSecurity(t *testing.T) {
	for _, c := range []Security{
		{Issuer: IssuerSecurity{Name: "tether"}},
		{TokenFile: "missing", Issuer: IssuerSecurity{URL: "http://remote.example"}},
	} {
		if _, err := NewServer("").WithSecurity(c); err == nil {
			t.Fatal("partial issuer configuration admitted")
		}
	}
}

func TestIssuerRejectsRedirectsAndUnsafeConfiguration(t *testing.T) {
	i := newTestIssuer(t)
	s, maintain := issuerServer(t, i)
	_ = s
	for _, c := range []IssuerSecurity{
		{Name: "tether", URL: "http://issuer.example", TokenFile: maintain},
		{Name: "", URL: i.server.URL, TokenFile: maintain},
		{Name: "tether", URL: i.server.URL, TokenFile: ""},
		{Name: "tether", URL: i.server.URL + "/unexpected", TokenFile: maintain},
		{Name: "tether", URL: "https://user:password@issuer.example", TokenFile: maintain},
	} {
		if _, err := newIssuerVerifier(c); err == nil {
			t.Fatalf("unsafe config accepted: %s", c.URL)
		}
	}
	var leakMu sync.Mutex
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakMu.Lock()
		leaked = true
		leakMu.Unlock()
		w.WriteHeader(200)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	verifier, err := newIssuerVerifier(IssuerSecurity{Name: "tether", URL: redirect.URL, TokenFile: maintain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), i.token(t, nil)); err != auth.ErrUnavailable {
		t.Fatalf("redirect error %v", err)
	}
	leakMu.Lock()
	defer leakMu.Unlock()
	if leaked {
		t.Fatal("issuer credential followed redirect")
	}
}

func TestServiceCaptureConsumerFixture(t *testing.T) {
	i := newTestIssuer(t)
	s, _ := issuerServer(t, i)
	raw := readCaptureAPIFixture(t, "valid-capture-service.json")
	first := meshCaptureRequest(s.Handler(), "POST", "/v1/captures", i.token(t, nil), raw)
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureResponse, first.Body.Bytes())
	fixture := readCaptureAPIFixture(t, "valid-capture-service-response.json")
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureResponse, fixture)
	var response capturecontract.CaptureManifestResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ReaderItem.ReadingState.PrincipalID != "svc:basalisk" || response.ReaderItem.Article.PreviewMarkdown != "Example note body." {
		t.Fatal("service note lost verified attribution or prefetched body")
	}
	completion := []byte(`{"schema_version":"fe.capture.completion.v1","idempotency_key":"complete-basalisk-note-01","assets":[],"warnings":[]}`)
	done := meshCaptureRequest(s.Handler(), "POST", "/v1/captures/basalisk-note-01/complete", i.token(t, nil), completion)
	if done.Code != 200 {
		t.Fatal(done.Code, done.Body.String())
	}
	assertCaptureAPIContract(t, capturecontract.SchemaCaptureStatus, done.Body.Bytes())
}
