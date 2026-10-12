package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	TokenUseAccess  = "access"
	TokenUseRefresh = "refresh"
)

func (m *Manager) signToken(principal Principal, sessionID, platform string, authVersion int64, jti, tokenUse string, issuedAt, expiresAt time.Time) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.cfg.Issuer,
			Audience:  jwt.ClaimStrings(m.cfg.Audience),
			Subject:   principal.Subject,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ID:        jti,
		},
		TokenUse:      tokenUse,
		SessionID:     sessionID,
		PrincipalType: principal.Type,
		TenantID:      principal.TenantID,
		Provider:      principal.Provider,
		Platform:      platform,
		AuthVersion:   authVersion,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *Manager) parseToken(raw, expectedUse string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidSignature
		}
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.cfg.Issuer),
		jwt.WithAudience(m.cfg.Audience...),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(m.cfg.ClockSkew),
		jwt.WithTimeFunc(m.currentTime),
	)
	if err != nil {
		return nil, mapJWTError(err)
	}
	if token == nil || !token.Valid {
		return nil, ErrTokenMalformed
	}
	if claims.TokenUse != expectedUse {
		return nil, ErrTokenUseMismatch
	}
	if claims.Subject == "" || claims.SessionID == "" || claims.ID == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return nil, ErrTokenMalformed
	}
	if !claims.ExpiresAt.Time.After(claims.IssuedAt.Time) {
		return nil, ErrTokenMalformed
	}
	if expectedUse == TokenUseRefresh && claims.TokenUse != TokenUseRefresh {
		return nil, ErrRefreshTokenInvalid
	}
	return claims, nil
}

func mapJWTError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	case errors.Is(err, jwt.ErrTokenExpired):
		return fmt.Errorf("%w: %v", ErrTokenExpired, err)
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return fmt.Errorf("%w: %v", ErrInvalidIssuer, err)
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return fmt.Errorf("%w: %v", ErrInvalidAudience, err)
	case errors.Is(err, jwt.ErrTokenMalformed), errors.Is(err, jwt.ErrTokenUnverifiable), errors.Is(err, jwt.ErrTokenNotValidYet):
		return fmt.Errorf("%w: %v", ErrTokenMalformed, err)
	default:
		return fmt.Errorf("%w: %v", ErrTokenMalformed, err)
	}
}
