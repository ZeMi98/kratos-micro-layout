package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

// AuthClaims carries the authenticated identity through the request context.
// Services populate it via their TokenVerifier implementation; downstream
// handlers read it with ClaimsFromContext / UserIDFromContext.
type AuthClaims struct {
	UserID   string
	Username string
}

// TokenVerifier verifies a bearer access token and returns the caller identity.
// Each service's biz layer (or a thin adapter in server/) implements this
// interface so the middleware stays service-agnostic.
type TokenVerifier interface {
	VerifyAccessToken(token string) (*AuthClaims, error)
}

// authorizationHeader carries the bearer access token.
const authorizationHeader = "Authorization"

// BearerFrom extracts the access token from the Authorization header of the
// current request, stripping the "Bearer " scheme. It returns "" when the
// header is absent or not a bearer token.
func BearerFrom(ctx context.Context) string {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return ""
	}
	v := tr.RequestHeader().Get(authorizationHeader)
	const prefix = "Bearer "
	if len(v) >= len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return strings.TrimSpace(v)
}

// TokenAuth returns a middleware that authenticates requests via bearer token.
// On success it attaches the verified claims to the context (readable with
// ClaimsFromContext / UserIDFromContext). On failure — missing token or
// verification error — it returns unauthorizedErr directly.
//
// Wrap it with selector.Server(...).Prefix(...).Path(...).Build() to exempt
// public routes per service.
func TokenAuth(verifier TokenVerifier, unauthorizedErr error) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			token := BearerFrom(ctx)
			if token == "" {
				return nil, unauthorizedErr
			}
			claims, err := verifier.VerifyAccessToken(token)
			if err != nil {
				return nil, err
			}
			return handler(NewAuthContext(ctx, claims), req)
		}
	}
}

// authContextKey is the private context key under which the auth middleware
// stores the verified claims. A dedicated unexported type avoids collisions
// with any other value placed in the context.
type authContextKey struct{}

// NewAuthContext returns a context carrying the authenticated claims. The auth
// middleware calls it once a token is verified; handlers read the identity back
// with ClaimsFromContext / UserIDFromContext instead of re-parsing the token.
func NewAuthContext(ctx context.Context, claims *AuthClaims) context.Context {
	if claims == nil {
		return ctx
	}
	return context.WithValue(ctx, authContextKey{}, claims)
}

// ClaimsFromContext returns the claims attached by the auth middleware. The
// second result is false when the request was not authenticated.
func ClaimsFromContext(ctx context.Context) (*AuthClaims, bool) {
	claims, ok := ctx.Value(authContextKey{}).(*AuthClaims)
	return claims, ok
}

// UserIDFromContext returns the authenticated caller's user id parsed as int64.
// It reports false when the context carries no valid identity or the UserID
// field is not a valid integer.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(claims.UserID, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
