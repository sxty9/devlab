package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testVerifier builds a verifier over a throwaway secret, without dev-bypass.
func testVerifier(t *testing.T) *Verifier {
	t.Helper()
	t.Setenv("HOLISTIC_SECRET", "bearer-test-secret")
	t.Setenv("DEVLAB_PREVIEW_SECRET_FILE", "")
	t.Setenv("HOLISTIC_SECRET_FILE", "")
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "")
	v := New()
	if !v.HasSecret() {
		t.Fatal("verifier has no secret")
	}
	return v
}

// currentUsername is the one Linux account a test can rely on existing: the one running the test.
// The verifier resolves groups live from the OS, so tokens must name a real account.
func currentUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Username == "" {
		t.Skip("no current OS user to build a session for")
	}
	return u.Username
}

func signToken(t *testing.T, v *Verifier, sub, kind string, exp time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": sub, "type": kind, "exp": time.Now().Add(exp).Unix()}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(v.secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cookieReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: accessCookie, Value: token})
	}
	return r
}

func bearerReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// A bearer token is exactly as good as the cookie — same user, same groups, same admin flag.
func TestBearerIsEquivalentToCookie(t *testing.T) {
	v := testVerifier(t)
	name := currentUsername(t)
	tok := signToken(t, v, name, "access", AccessTTL)

	viaCookie, err := v.FromRequest(cookieReq(tok))
	if err != nil {
		t.Fatalf("cookie path: %v", err)
	}
	viaBearer, err := v.FromRequest(bearerReq(tok))
	if err != nil {
		t.Fatalf("bearer path: %v", err)
	}
	if viaBearer.Username != viaCookie.Username || viaBearer.IsAdmin != viaCookie.IsAdmin ||
		len(viaBearer.Groups) != len(viaCookie.Groups) {
		t.Fatalf("bearer identity %+v differs from cookie identity %+v", viaBearer, viaCookie)
	}
	if !BearerPresented(bearerReq(tok)) {
		t.Fatal("BearerPresented must report a presented bearer token")
	}
	if BearerPresented(cookieReq(tok)) {
		t.Fatal("BearerPresented must not report a cookie-only request")
	}
}

// Everything the cookie path rejects, the bearer path rejects too — one verifier, one verdict.
func TestBearerRejectsWhatTheCookiePathRejects(t *testing.T) {
	v := testVerifier(t)
	name := currentUsername(t)

	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"garbage", "not-a-token"},
		{"refresh token", signToken(t, v, name, "refresh", time.Hour)},
		{"expired", signToken(t, v, name, "access", -time.Minute)},
		{"unknown account", signToken(t, v, "nosuchuser-devlab-test", "access", AccessTTL)},
		{"foreign signature", func() string {
			s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"sub": name, "type": "access", "exp": time.Now().Add(time.Hour).Unix(),
			}).SignedString([]byte("a different secret"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}()},
	}
	for _, c := range cases {
		if _, err := v.FromRequest(cookieReq(c.token)); err == nil {
			t.Fatalf("%s: cookie path accepted it", c.name)
		}
		if _, err := v.FromRequest(bearerReq(c.token)); err == nil {
			t.Fatalf("%s: bearer path accepted it", c.name)
		}
	}
}

// A missing secret fails closed on BOTH paths: an empty HMAC key would otherwise validate tokens
// signed with the empty key. The verifier is built directly in the state New() leaves it in when
// no secret is readable, so the invariant holds regardless of what the test machine has installed.
func TestNoSecretFailsClosedOnBothPaths(t *testing.T) {
	v := &Verifier{adminGroup: "sudo"}
	if v.HasSecret() {
		t.Fatal("a verifier without a secret must report so")
	}
	// A token signed with the empty key — the exact forgery an empty HMAC key would accept.
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "root", "type": "access", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*http.Request{"cookie": cookieReq(forged), "bearer": bearerReq(forged)} {
		if _, err := v.FromRequest(r); err == nil {
			t.Fatalf("%s path accepted a token although no signing secret is configured", name)
		}
	}
}

// And the daemon does not come up in that state at all: SSO mode without a readable secret is a
// refusal to start, not a degraded mode (fail-closed, B-05).
func TestDaemonRefusesToStartWithoutSecret(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "devlabd", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "HasSecret()") {
		t.Fatal("the daemon must consult HasSecret() before serving")
	}
	at := strings.Index(s, "HasSecret()")
	if !strings.Contains(s[at:min(at+400, len(s))], "log.Fatal") {
		t.Error("a missing session secret must abort the start (fail closed), not warn and continue")
	}
}

// Only the Bearer scheme counts, and only token-shaped values are passed on — a header cannot
// smuggle anything into the carrier the verifier reads.
func TestBearerHeaderShape(t *testing.T) {
	for _, h := range []string{
		"",
		"Basic abc",
		"Bearer ",
		"Bearer with space",
		"Bearer has;semicolon",
		"Bearer \"quoted\"",
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/user", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		if BearerPresented(r) {
			t.Fatalf("header %q must not count as a presented bearer token", h)
		}
	}
	// The scheme name is case-insensitive per RFC 7235.
	r := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	r.Header.Set("Authorization", "bearer abc.def.ghi")
	if !BearerPresented(r) {
		t.Fatal("lower-case scheme must be accepted")
	}
}
