package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

// Security configures the owner credential and optional scoped mesh issuer.
// BrowserOrigins lists
// public HTTPS origins whose Host may be rewritten by the local reverse proxy,
// or exact Chrome extension origins for explicitly authorized browser clients.
// It is an origin admission list, not a cross-origin browser API (no CORS).
type Security struct {
	TokenFile      string
	BrowserOrigins []string
	Issuer         IssuerSecurity
}

// AuthApp names the posture env var: HOLLIS_AUTH_FRAGMENTS_ENGINE.
const AuthApp = "fragments-engine"

const sessionCookie = "fe_session"
const sessionLifetime = 8 * time.Hour

type browserSession struct {
	expires        time.Time
	credentialHash string
}

type httpSecurity struct {
	watch    *tokenfile.Watcher
	guard    *auth.Guard
	mode     auth.Mode
	issuer   *issuerVerifier
	origins  map[string]bool
	mu       sync.Mutex
	sessions map[string]browserSession // indexed by hash, never the cookie value
}

// WithSecurity must be called before serving. A configured but unreadable or
// unsafe token file is an error; it must never silently disable authentication.
func (s *Server) WithSecurity(c Security) (*Server, error) {
	if c.TokenFile == "" {
		if c.Issuer != (IssuerSecurity{}) {
			return nil, errors.New("issuer configuration requires the existing owner token file")
		}
		return s, nil
	}
	sec := &httpSecurity{
		watch:   tokenfile.NewWatcher(c.TokenFile, tokenfile.Options{Validate: validAPIToken}, nil),
		origins: make(map[string]bool), sessions: make(map[string]browserSession),
	}
	if _, err := sec.watch.Token(); err != nil {
		return nil, fmt.Errorf("API token file: %w", err)
	}
	for _, origin := range c.BrowserOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Host)) && !isChromeExtensionOrigin(u)) {
			return nil, fmt.Errorf("invalid browser origin %q: use https://host[:port], loopback http, or chrome-extension://<32-character extension ID>", origin)
		}
		sec.origins[origin] = true
	}
	if c.Issuer != (IssuerSecurity{}) {
		var err error
		sec.issuer, err = newIssuerVerifier(c.Issuer)
		if err != nil {
			return nil, err
		}
	}
	// Posture dial: HOLLIS_AUTH_FRAGMENTS_ENGINE, then HOLLIS_AUTH; unset keeps enforce.
	mode, err := auth.ModeFromEnv(AuthApp, auth.ModeEnforce)
	if err != nil {
		return nil, err
	}
	sec.mode = mode
	g, err := auth.New(auth.Config{Mode: mode, Verifier: sec,
		OpenPath: func(string) bool { return false },
		Scopes:   apiScopes(),
	})
	if err != nil {
		return nil, err
	}
	sec.guard = g
	s.security = sec
	return s, nil
}

func isChromeExtensionOrigin(u *url.URL) bool {
	if u.Scheme != "chrome-extension" || len(u.Host) != 32 {
		return false
	}
	for _, c := range u.Host {
		if c < 'a' || c > 'p' {
			return false
		}
	}
	return true
}

func validAPIToken(token string) bool {
	if len(token) < 32 || len(token) > 256 {
		return false
	}
	for _, c := range token {
		if c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

// Verify adapts the rotating private token file to the shared auth guard.
func (s *httpSecurity) Verify(ctx context.Context, presented string) (auth.Principal, error) {
	token, err := s.watch.Token()
	if err == nil {
		p, verifyErr := auth.NewAPIKeyVerifier(auth.APIKey{Hash: auth.HashKey(token),
			ID: "fragments-engine/owner", Scopes: []string{"fragments-engine.*"},
		}).Verify(ctx, presented)
		if verifyErr == nil {
			return p, nil
		}
	}
	if strings.Count(presented, ".") == 2 && s.issuer != nil {
		return s.issuer.Verify(ctx, presented)
	}
	if err != nil {
		return auth.Principal{}, auth.ErrUnavailable
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

func (s *Server) validateListen(addr string) error {
	if s.security != nil && s.security.mode != auth.ModeEnforce && !isLoopbackAddr(addr) {
		return fmt.Errorf("auth mode %q is only honoured on a loopback listener; %q is not loopback (use enforce)", s.security.mode, addr)
	}
	if s.security == nil && !isLoopbackAddr(addr) {
		return errors.New("non-loopback API listener requires --token-file or FRAGMENTS_API_TOKEN_FILE")
	}
	return checkTranscriptListen(s.cfgPath, addr)
}

func (s *httpSecurity) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if s.origins[origin] {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}

func (s *Server) secureHandler(next http.Handler) http.Handler {
	sec := s.security
	var guarded http.Handler
	if sec != nil {
		guarded = sec.guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/auth/session" || strings.HasPrefix(r.URL.Path, "/v1/transcripts/") {
				p, _ := auth.FromContext(r.Context())
				if p.ID != "fragments-engine/owner" || p.Audience != "" {
					http.Error(w, "owner credential required", http.StatusForbidden)
					return
				}
			}
			if r.URL.Path == "/v1/auth/session" {
				sec.handleSession(w, r)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/v1/auth/status" && r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]bool{"enabled": sec != nil})
			return
		}
		if sec == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !sec.originAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		// A supplied Authorization header always wins, including malformed headers.
		// Cookies cannot rescue an invalid explicit credential or create a session.
		if len(r.Header.Values("Authorization")) == 0 && !(r.URL.Path == "/v1/auth/session" && r.Method == http.MethodPost) {
			if c, err := r.Cookie(sessionCookie); err == nil {
				if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
					http.Error(w, "same-origin session required", http.StatusForbidden)
					return
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") == "" {
					http.Error(w, "browser session writes require Origin", http.StatusForbidden)
					return
				}
				if token := sec.sessionToken(c.Value); token != "" {
					r = r.Clone(r.Context())
					r.Header.Set("Authorization", "Bearer "+token)
				}
			}
		}
		guarded.ServeHTTP(w, r)
	})
}

func (s *httpSecurity) sessionToken(cookie string) string {
	token, err := s.watch.Token()
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := auth.HashKey(cookie)
	session, ok := s.sessions[key]
	if !ok || !time.Now().Before(session.expires) || session.credentialHash != auth.HashKey(token) {
		delete(s.sessions, key)
		return ""
	}
	return token
}

func (s *httpSecurity) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		// Only explicit bearer authentication reaches this handler on POST.
		token, err := s.watch.Token()
		if err != nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		// Do not issue a session if the credential rotated after the guard ran.
		presented, _ := auth.ParseBearer(r.Header.Values("Authorization"))
		if !s.watch.Verify(presented) || auth.HashKey(presented) != auth.HashKey(token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		value, err := tokenfile.Generate()
		if err != nil {
			http.Error(w, "session unavailable", http.StatusInternalServerError)
			return
		}
		now := time.Now()
		s.mu.Lock()
		for key, session := range s.sessions {
			if !now.Before(session.expires) || session.credentialHash != auth.HashKey(token) {
				delete(s.sessions, key)
			}
		}
		if old, err := r.Cookie(sessionCookie); err == nil {
			delete(s.sessions, auth.HashKey(old.Value))
		}
		if len(s.sessions) >= 64 {
			s.mu.Unlock()
			http.Error(w, "too many browser sessions", http.StatusTooManyRequests)
			return
		}
		s.sessions[auth.HashKey(value)] = browserSession{expires: now.Add(sessionLifetime), credentialHash: auth.HashKey(token)}
		s.mu.Unlock()
		http.SetCookie(w, s.cookie(r, value, int(sessionLifetime.Seconds())))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if c, err := r.Cookie(sessionCookie); err == nil {
			s.mu.Lock()
			delete(s.sessions, auth.HashKey(c.Value))
			s.mu.Unlock()
		}
		http.SetCookie(w, s.cookie(r, "", -1))
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *httpSecurity) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	// Only an allowed HTTPS Origin or a direct TLS connection selects Secure.
	// Arbitrary forwarded headers never establish trust or admission.
	secure := r.TLS != nil || strings.HasPrefix(r.Header.Get("Origin"), "https://")
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/v1/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
}
