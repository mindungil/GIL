package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mockIDP serves /.well-known/openid-configuration and /jwks for a single
// generated RSA keypair. It hands back a `mintToken` helper bound to that
// keypair so each test can issue arbitrary claims without re-deriving the JWK.
type mockIDP struct {
	server   *httptest.Server
	priv     *rsa.PrivateKey
	pub      *rsa.PublicKey
	kid      string
	issuer   string
	jwksURI  string
	jwksJSON []byte
}

// newMockIDP builds an httptest server that behaves like an OIDC issuer for
// a single RS256 key. issuerOverride lets tests inject a wrong-issuer scenario;
// when "" we use the live server URL.
func newMockIDP(t *testing.T, kid string) *mockIDP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &mockIDP{priv: priv, pub: &priv.PublicKey, kid: kid}

	// Build the JWKS document up front; it doesn't change between requests
	// in this test fixture.
	nB64 := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.PublicKey.E)).Bytes())
	jwksDoc := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"kid": kid,
			"n":   nB64,
			"e":   eB64,
		}},
	}
	idp.jwksJSON, err = json.Marshal(jwksDoc)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   idp.issuer,
			"jwks_uri": idp.jwksURI,
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(idp.jwksJSON)
	})

	idp.server = httptest.NewServer(mux)
	idp.issuer = idp.server.URL
	idp.jwksURI = idp.server.URL + "/jwks"
	t.Cleanup(idp.server.Close)
	return idp
}

// mintToken builds and signs a compact JWT with the IdP's keypair. The
// caller controls the claims map entirely so tests can inject expired tokens,
// wrong audience, etc., without any hidden defaults.
func (i *mockIDP) mintToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	return i.mintTokenAlg(t, "RS256", i.kid, claims)
}

// mintTokenAlg gives finer-grained control: arbitrary alg, kid, and claims.
// Used to test "unknown kid" and "alg mismatch" cases.
func (i *mockIDP) mintTokenAlg(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT", "kid": kid}
	hb, err := json.Marshal(header)
	require.NoError(t, err)
	cb, err := json.Marshal(claims)
	require.NoError(t, err)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)

	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.priv, crypto.SHA256, hashed[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newTestVerifier builds a Verifier wired to the mock IdP, ready to verify
// tokens minted via mintToken.
func newTestVerifier(t *testing.T, idp *mockIDP, audience string) *Verifier {
	t.Helper()
	v, err := NewVerifier(context.Background(), idp.issuer, audience, time.Minute, nil)
	require.NoError(t, err)
	return v
}

// ---------------------------------------------------------------------------
// Verifier-only tests (no gRPC server needed).
// ---------------------------------------------------------------------------

func TestVerifier_ValidToken(t *testing.T) {
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	now := time.Now().Unix()
	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "user-42",
		"iat": now,
		"exp": now + 600,
	})

	c, err := v.Verify(context.Background(), tok)
	require.NoError(t, err)
	require.Equal(t, "user-42", c.Sub)
	require.Equal(t, idp.issuer, c.Iss)
}

func TestVerifier_ExpiredToken(t *testing.T) {
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "user-42",
		"exp": time.Now().Add(-2 * time.Hour).Unix(),
	})
	_, err := v.Verify(context.Background(), tok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
}

func TestVerifier_WrongAudience(t *testing.T) {
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "some-other-app",
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(context.Background(), tok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "aud")
}

func TestVerifier_WrongIssuer(t *testing.T) {
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	tok := idp.mintToken(t, map[string]any{
		"iss": "https://attacker.example.com",
		"aud": "gil-test",
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(context.Background(), tok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "iss")
}

func TestVerifier_UnknownKid(t *testing.T) {
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	tok := idp.mintTokenAlg(t, "RS256", "ghost-kid", map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(context.Background(), tok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "kid")
}

func TestVerifier_AudList(t *testing.T) {
	// RFC 7519: aud may be a string OR an array of strings.
	idp := newMockIDP(t, "test-key-1")
	v := newTestVerifier(t, idp, "gil-test")
	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": []string{"some-other", "gil-test", "yet-another"},
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(context.Background(), tok)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Middleware tests against an in-process gRPC server using the standard
// health service as a stand-in for "any RPC". This keeps the test focused
// on the auth layer rather than gild's domain RPCs.
// ---------------------------------------------------------------------------

// startTestServer spins up an in-process gRPC server with the health service
// registered behind the auth middleware. It returns the listener address +
// a cleanup func. network is "tcp" or "unix".
func startTestServer(t *testing.T, network string, mw *Middleware) (string, func()) {
	t.Helper()
	var lis net.Listener
	var addr string
	var err error
	if network == "unix" {
		sock := filepath.Join(t.TempDir(), "gild.sock")
		lis, err = net.Listen("unix", sock)
		require.NoError(t, err)
		addr = "unix:" + sock
	} else {
		lis, err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr = lis.Addr().String()
	}

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(mw.UnaryInterceptor()),
		grpc.StreamInterceptor(mw.StreamInterceptor()),
	)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()

	cleanup := func() {
		srv.Stop()
		_ = lis.Close()
	}
	return addr, cleanup
}

// dialTestServer connects to addr (which may be "unix:..." or "host:port").
func dialTestServer(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if len(addr) > 5 && addr[:5] == "unix:" {
		path := addr[5:]
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}))
	}
	conn, err := grpc.NewClient(addr, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestMiddleware_TCP_ValidToken(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: true}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)

	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
	_, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
}

func TestMiddleware_TCP_ExpiredToken(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: true}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)

	tok := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "u1",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
	_, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestMiddleware_TCP_MissingToken(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: true}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)
	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestMiddleware_UDS_NoTokenAllowed(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: true}
	addr, cleanup := startTestServer(t, "unix", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)

	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "UDS connection with AllowUDS=true must bypass auth")
}

func TestMiddleware_UDS_DisallowedRequiresToken(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: false}
	addr, cleanup := startTestServer(t, "unix", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)
	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err, "UDS connection with AllowUDS=false must still require a token")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestMiddleware_TCP_UnknownKid(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{Verifier: newTestVerifier(t, idp, "gil-test"), AllowUDS: true}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)

	tok := idp.mintTokenAlg(t, "RS256", "phantom", map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
	_, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestMiddleware_TCP_EnforceUserSub(t *testing.T) {
	idp := newMockIDP(t, "k1")
	mw := &Middleware{
		Verifier:       newTestVerifier(t, idp, "gil-test"),
		AllowUDS:       true,
		EnforceUserSub: "alice",
	}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)

	wrong := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "bob",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+wrong)
	_, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	right := idp.mintToken(t, map[string]any{
		"iss": idp.issuer,
		"aud": "gil-test",
		"sub": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	ctx = metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+right)
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
}

func TestMiddleware_NilVerifier_PassesThrough(t *testing.T) {
	// A defensive test: gild constructs the middleware only when --auth-issuer
	// is set, but if a nil one ever leaks through (e.g. in a bad refactor), it
	// must not crash — it should treat auth as disabled.
	mw := &Middleware{}
	addr, cleanup := startTestServer(t, "tcp", mw)
	defer cleanup()
	conn := dialTestServer(t, addr)
	client := healthpb.NewHealthClient(conn)
	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
}

