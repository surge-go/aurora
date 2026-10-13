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

// signToken 使用 HS256 为指定 Principal 签发 JWT。
// claims 同时包含会话标识（SessionID）、用途（TokenUse）、平台与版本信息，
// 后续在 Verify / Refresh 中会与 Redis 中的会话状态比对。
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

// parseToken 解析并校验 JWT，强制限定签名算法、issuer、audience、过期与签发时间。
// 除加密校验外还会做语义检查：subject/sessionID/JTI 必须存在，expires 必须晚于 iat，
// token_use 必须与调用方期望一致。
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

// mapJWTError 将 jwt 库的底层错误映射为本包的稳定错误，
// 让调用方可以通过 errors.Is 进行跨层判断。
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
