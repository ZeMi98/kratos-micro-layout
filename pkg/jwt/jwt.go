// Package jwt provides a reusable HMAC (HS256) token engine for issuing and
// verifying access/refresh tokens. It is deliberately service-agnostic: any
// service in the monorepo that shares the same secret and issuer can mint or
// verify tokens, so a token issued by the user center can be validated by a
// downstream service without duplicating the signing logic.
//
// The engine owns only the cryptographic concern — sign, verify, parse. The
// surrounding business rules (who may log in, how passwords are checked, which
// API error a bad token maps to) stay in each service's biz layer, which wraps
// a *Manager and adapts its output to the service's own types.
package jwt

import (
	"time"

	golangjwt "github.com/golang-jwt/jwt/v5"
)

// TokenType distinguishes access tokens from refresh tokens. Embedding it in
// Claims lets a single verifier reject a refresh token presented where an
// access token is required.
type TokenType int

const (
	// TokenTypeAccess marks a short-lived token that authorizes API calls.
	TokenTypeAccess TokenType = 0
	// TokenTypeRefresh marks a long-lived token used only to obtain a new
	// access/refresh pair.
	TokenTypeRefresh TokenType = 1
)

// Default token lifetimes applied when Options leaves a TTL unset.
const (
	defaultAccessTTL  = 2 * time.Hour
	defaultRefreshTTL = 168 * time.Hour // 7 days
)

// Claims is the JWT payload. The subject stays a string: the claim is compact
// and JS clients read it back without 64-bit precision loss.
type Claims struct {
	golangjwt.RegisteredClaims
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	TokenType TokenType `json:"token_type"`
}

// Options configures a Manager. Secret and Issuer should be set; the TTLs fall
// back to sane defaults when zero.
type Options struct {
	// Secret is the HMAC signing key. Every service that verifies these tokens
	// must use the same secret.
	Secret string
	// Issuer is written to the "iss" claim so a verifier can tell which service
	// minted a token (e.g. "user_center").
	Issuer string
	// AccessTokenTTL is the access token lifetime. Defaults to 2h when zero.
	AccessTokenTTL time.Duration
	// RefreshTokenTTL is the refresh token lifetime. Defaults to 168h when zero.
	RefreshTokenTTL time.Duration
}

// Manager issues and verifies HS256 tokens. A Manager is immutable after
// construction and safe for concurrent use.
type Manager struct {
	secret          []byte
	issuer          string
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// NewManager builds a Manager from opts, applying default TTLs where unset.
func NewManager(opts Options) *Manager {
	accessTTL := opts.AccessTokenTTL
	if accessTTL == 0 {
		accessTTL = defaultAccessTTL
	}
	refreshTTL := opts.RefreshTokenTTL
	if refreshTTL == 0 {
		refreshTTL = defaultRefreshTTL
	}
	return &Manager{
		secret:          []byte(opts.Secret),
		issuer:          opts.Issuer,
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

// GenerateAccessToken creates a short-lived access token.
func (m *Manager) GenerateAccessToken(userID, username string) (string, error) {
	return m.generate(userID, username, TokenTypeAccess, m.accessTokenTTL)
}

// GenerateRefreshToken creates a long-lived refresh token.
func (m *Manager) GenerateRefreshToken(userID, username string) (string, error) {
	return m.generate(userID, username, TokenTypeRefresh, m.refreshTokenTTL)
}

// generate signs a token of the given type and lifetime. Both public helpers
// share it so the claim layout can never drift between access and refresh.
func (m *Manager) generate(userID, username string, tokenType TokenType, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: golangjwt.RegisteredClaims{
			ExpiresAt: golangjwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  golangjwt.NewNumericDate(now),
			NotBefore: golangjwt.NewNumericDate(now),
			Issuer:    m.issuer,
		},
		UserID:    userID,
		Username:  username,
		TokenType: tokenType,
	}
	token := golangjwt.NewWithClaims(golangjwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// ParseToken validates the signature and expiry of a token string and returns
// its claims. It does not check TokenType — callers decide whether an access or
// refresh token is acceptable in their context.
func (m *Manager) ParseToken(tokenString string) (*Claims, error) {
	token, err := golangjwt.ParseWithClaims(tokenString, &Claims{}, func(*golangjwt.Token) (interface{}, error) {
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, golangjwt.ErrTokenInvalidClaims
	}
	return claims, nil
}

// AccessTokenTTL returns the access token lifetime in seconds, suitable for the
// expires_in field of a token response.
func (m *Manager) AccessTokenTTL() int64 {
	return int64(m.accessTokenTTL.Seconds())
}
