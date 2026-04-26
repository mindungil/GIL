package auth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Middleware enforces bearer-token OIDC authentication on gRPC requests.
//
// Behaviour:
//   - If the connection peer is on the unix transport AND AllowUDS is true,
//     the request is allowed through without a token (UDS = local trust via
//     filesystem perms; see server/internal/uds/listener.go for the mode 0600
//     on the socket file).
//   - Otherwise the request must carry "authorization: Bearer <jwt>" in its
//     incoming metadata. The token is validated with Verifier; on success
//     the parsed Claims are injected into the context (auth.WithClaims) so
//     downstream service handlers can use them.
//   - Any verification failure or missing token returns codes.Unauthenticated.
//
// EnforceUserSub, when non-empty, additionally requires claims.Sub to equal
// the configured value — this gives the daemon a "this gild instance only
// serves user X" assertion mode that pairs with --user namespacing.
type Middleware struct {
	Verifier       *Verifier
	AllowUDS       bool
	EnforceUserSub string
}

// UnaryInterceptor returns a gRPC UnaryServerInterceptor implementing the
// auth policy described on Middleware.
func (m *Middleware) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		newCtx, err := m.authenticate(ctx)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamInterceptor returns a gRPC StreamServerInterceptor mirroring the
// unary policy. The wrapped stream's Context() returns the post-auth context,
// so service handlers see Claims via auth.ClaimsFromContext.
func (m *Middleware) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := m.authenticate(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &authedServerStream{ServerStream: ss, ctx: newCtx})
	}
}

// authedServerStream overlays a custom context on a gRPC ServerStream so
// downstream handlers see the auth-enriched ctx via stream.Context().
type authedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (a *authedServerStream) Context() context.Context { return a.ctx }

// authenticate is the shared policy core for both unary and stream paths.
func (m *Middleware) authenticate(ctx context.Context) (context.Context, error) {
	if m == nil || m.Verifier == nil {
		// Defensive: a nil middleware is treated as auth-disabled. The wiring
		// in main.go avoids constructing one when --auth-issuer is empty, so
		// this branch is mostly for tests.
		return ctx, nil
	}

	if m.AllowUDS && peerIsUDS(ctx) {
		return ctx, nil
	}

	token, err := bearerFromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	claims, err := m.Verifier.Verify(ctx, token)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "auth: %v", err)
	}

	if m.EnforceUserSub != "" && claims.Sub != m.EnforceUserSub {
		return nil, status.Errorf(codes.PermissionDenied,
			"auth: token sub %q does not match enforced user %q", claims.Sub, m.EnforceUserSub)
	}

	return WithClaims(ctx, claims), nil
}

// peerIsUDS returns true when the gRPC peer is connected over a Unix socket.
// We rely on the standard library's net.Addr.Network() == "unix".
func peerIsUDS(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return false
	}
	return p.Addr.Network() == "unix"
}

// bearerFromContext extracts a Bearer token from incoming gRPC metadata.
// Returns an error suitable for the Unauthenticated reply when missing.
func bearerFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", errMissingBearer
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", errMissingBearer
	}
	const prefix = "Bearer "
	for _, v := range values {
		if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
			return strings.TrimSpace(v[len(prefix):]), nil
		}
	}
	return "", errMalformedBearer
}

// Sentinel errors returned to clients via Unauthenticated status. Kept as
// concrete strings (not wrapped) so they're easy to assert against in tests.
var (
	errMissingBearer   = strErr("missing authorization bearer token")
	errMalformedBearer = strErr("authorization header must be 'Bearer <token>'")
)

type strErr string

func (e strErr) Error() string { return string(e) }
