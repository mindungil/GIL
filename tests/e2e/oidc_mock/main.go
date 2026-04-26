// oidc_mock is a tiny standalone OIDC issuer used by the Phase 10 Track E
// e2e test. It generates an RSA keypair on startup, exposes the OIDC
// discovery + JWKS endpoints, and mints two tokens (one valid, one expired)
// to disk so the test harness can hand them to a gRPC client.
//
// Usage:
//
//	oidc_mock -addr :7071 -audience gil-test -outdir /tmp/oidc-mock
//
// The binary blocks until SIGINT/SIGTERM. On startup it writes:
//
//	$outdir/issuer.txt        — issuer URL (http://127.0.0.1:<port>)
//	$outdir/valid.jwt         — RS256 token, exp = now + 1h
//	$outdir/expired.jwt       — RS256 token, exp = now - 1h
//
// Stdlib only.
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen addr")
	audience := flag.String("audience", "gil-test", "token aud claim")
	subject := flag.String("subject", "e2e-user", "token sub claim")
	outdir := flag.String("outdir", "", "directory to write issuer.txt + token files (required)")
	flag.Parse()

	if *outdir == "" {
		log.Fatal("oidc_mock: -outdir required")
	}
	if err := os.MkdirAll(*outdir, 0o700); err != nil {
		log.Fatalf("oidc_mock: mkdir outdir: %v", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("oidc_mock: rsa keygen: %v", err)
	}
	const kid = "e2e-key-1"

	// Bind the listener up front so we can compute the issuer URL even when
	// the user passed :0 (kernel-assigned port).
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("oidc_mock: listen: %v", err)
	}
	host, port, _ := net.SplitHostPort(lis.Addr().String())
	if host == "" || host == "::" {
		host = "127.0.0.1"
	}
	issuer := fmt.Sprintf("http://%s:%s", host, port)

	// Pre-build the JWKS document — it's fixed for the lifetime of the process.
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
	jwksBytes, _ := json.Marshal(jwksDoc)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   issuer,
			"jwks_uri": issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(jwksBytes)
	})

	now := time.Now().Unix()
	validTok := mintRS256(priv, kid, map[string]any{
		"iss": issuer,
		"aud": *audience,
		"sub": *subject,
		"iat": now,
		"exp": now + 3600,
	})
	expiredTok := mintRS256(priv, kid, map[string]any{
		"iss": issuer,
		"aud": *audience,
		"sub": *subject,
		"iat": now - 7200,
		"exp": now - 3600,
	})

	for _, f := range []struct {
		name string
		body string
	}{
		{"issuer.txt", issuer + "\n"},
		{"valid.jwt", validTok + "\n"},
		{"expired.jwt", expiredTok + "\n"},
		{"audience.txt", *audience + "\n"},
		{"subject.txt", *subject + "\n"},
	} {
		if err := os.WriteFile(filepath.Join(*outdir, f.name), []byte(f.body), 0o600); err != nil {
			log.Fatalf("oidc_mock: write %s: %v", f.name, err)
		}
	}

	srv := &http.Server{Handler: mux}
	go func() {
		log.Printf("oidc_mock: listening on %s (issuer=%s)", lis.Addr(), issuer)
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Fatalf("oidc_mock: serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("oidc_mock: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// mintRS256 builds a compact JWT with the given private key and claims.
func mintRS256(priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		log.Fatalf("oidc_mock: sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}
