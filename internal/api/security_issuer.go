package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

// IssuerSecurity pins Tether's identity independently from its transport URL.
// TokenFile is a dedicated Tether maintain credential, never the FE owner key.
// UnixSocket optionally connects to a local Tether socket instead of TCP.
type IssuerSecurity struct {
	Name             string
	URL              string
	TokenFile        string
	UnixSocket       string
	BootstrapKeyFile string
}

type issuerVerifier struct {
	jwt      *auth.JWTVerifier
	deny     *auth.DenyListPoller
	mu       sync.Mutex
	lastPoll time.Time
}

func newIssuerVerifier(c IssuerSecurity) (*issuerVerifier, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (isLoopbackHost(u.Host) || c.UnixSocket != ""))) || c.Name == "" || c.TokenFile == "" {
		return nil, fmt.Errorf("issuer requires a pinned name, HTTPS (or local HTTP) base URL and private maintain token file")
	}
	watch := tokenfile.NewWatcher(c.TokenFile, tokenfile.Options{Validate: validAPIToken}, nil)
	if _, err := watch.Token(); err != nil {
		return nil, fmt.Errorf("issuer credential file: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.UnixSocket != "" {
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.UnixSocket)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: issuerTransport{base: transport, watch: watch},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(c.URL, "/")
	list := auth.NewDenyList()
	jwt, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: c.Name, Audience: "fragments-engine", JWKSURL: base + "/.well-known/jwks.json", BootstrapKeyFile: c.BootstrapKeyFile, Client: client, DenyList: list})
	if err != nil {
		return nil, err
	}
	return &issuerVerifier{jwt: jwt, deny: &auth.DenyListPoller{URL: base + "/auth/denylist", List: list, Client: client}}, nil
}

type issuerTransport struct {
	base  http.RoundTripper
	watch *tokenfile.Watcher
}

func (t issuerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	token, err := t.watch.Token()
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(r)
}

func (v *issuerVerifier) Verify(ctx context.Context, token string) (auth.Principal, error) {
	// Never start with an empty, unconfirmed revocation list or accept a stale
	// list indefinitely. A poll failure closes JWT admission after 30 seconds;
	// the independent local owner credential remains available for recovery.
	v.mu.Lock()
	if time.Since(v.lastPoll) >= 30*time.Second {
		if err := v.deny.Poll(ctx); err != nil {
			v.mu.Unlock()
			return auth.Principal{}, auth.ErrUnavailable
		}
		v.lastPoll = time.Now()
	}
	v.mu.Unlock()
	return v.jwt.Verify(ctx, token)
}
