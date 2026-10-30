package api

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/libs/security/auth"
	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

func dialServer(t *testing.T, appEnv, genEnv string) (*Server, error) {
	t.Helper()
	t.Setenv("HOLLIS_AUTH_FRAGMENTS_ENGINE", appEnv)
	t.Setenv("HOLLIS_AUTH", genEnv)
	path := filepath.Join(t.TempDir(), "token")
	if err := tokenfile.Write(path, testAPIToken); err != nil {
		t.Fatal(err)
	}
	return NewServer("").WithSecurity(Security{TokenFile: path})
}

func TestAuthDialDefaultIsEnforce(t *testing.T) {
	s, err := dialServer(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.security.mode != auth.ModeEnforce {
		t.Fatalf("default mode %q", s.security.mode)
	}
	if w := securityRequest(s.secureHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })), "GET", "/v1/fragments", "", "", nil); w.Code != 401 {
		t.Fatalf("default must reject anonymous, got %d", w.Code)
	}
	if err := s.validateListen("0.0.0.0:8091"); err != nil {
		t.Fatal("enforce must allow non-loopback:", err)
	}
}

func TestAuthDialOffAndPermissive(t *testing.T) {
	for _, tc := range []struct {
		app, gen string
		want     auth.Mode
	}{
		{"off", "", auth.ModeOff}, {"permissive", "", auth.ModePermissive},
		{"", "off", auth.ModeOff}, {"enforce", "off", auth.ModeEnforce}, // app var wins over HOLLIS_AUTH
	} {
		s, err := dialServer(t, tc.app, tc.gen)
		if err != nil {
			t.Fatal(err)
		}
		if s.security.mode != tc.want {
			t.Fatalf("%+v: mode %q", tc, s.security.mode)
		}
		if tc.want != auth.ModeEnforce {
			if w := securityRequest(s.secureHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })), "GET", "/v1/fragments", "", "", nil); w.Code != 204 {
				t.Fatalf("%+v: status %d", tc, w.Code)
			}
			if err := s.validateListen("127.0.0.1:8091"); err != nil {
				t.Fatalf("loopback refused: %v", err)
			}
			for _, addr := range []string{"0.0.0.0:8091", ":8091", "100.64.0.5:8091"} {
				if err := s.validateListen(addr); err == nil {
					t.Fatalf("%v on %s must be refused", tc.want, addr)
				}
			}
		}
	}
}

func TestAuthDialInvalidRejected(t *testing.T) {
	if _, err := dialServer(t, "bogus", ""); err == nil {
		t.Fatal("invalid app value accepted")
	}
	if _, err := dialServer(t, "", "nope"); err == nil {
		t.Fatal("invalid HOLLIS_AUTH accepted")
	}
}
