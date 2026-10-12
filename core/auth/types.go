package auth

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Principal identifies an authenticated subject without coupling auth to a user table.
type Principal struct {
	Subject  string `json:"subject"`
	Type     string `json:"type,omitempty"`
	TenantID string `json:"tenant_id,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Claims contains the claims shared by access and refresh tokens.
type Claims struct {
	jwt.RegisteredClaims
	TokenUse      string `json:"token_use"`
	SessionID     string `json:"sid"`
	PrincipalType string `json:"principal_type,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Platform      string `json:"platform,omitempty"`
	AuthVersion   int64  `json:"auth_version,omitempty"`
}

type TokenPair struct {
	TokenType        string    `json:"token_type"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

// Session is the Redis-backed state associated with one login.
type Session struct {
	ID                 string
	Principal          Principal
	AuthVersion        int64
	CurrentRefreshHash string
	IssuedAt           time.Time
	LastRefreshedAt    time.Time
	ExpiresAt          time.Time
	AbsoluteExpiresAt  time.Time
	RevokedAt          time.Time
	Platform           string
	DeviceID           string
}

// IssueRequest contains the identity and client metadata for a new session.
type IssueRequest struct {
	Principal   Principal
	AuthVersion int64
	Platform    string
	DeviceID    string
	ForceLogin  bool
	// IdempotencyKey identifies one logical login attempt. Reusing it with the
	// same request returns the original token pair for a short configured TTL.
	IdempotencyKey string
}

// RevokePage reports one bounded subject-revocation operation.
type RevokePage struct {
	Revoked int
	HasMore bool
}

// RefreshValidator rechecks business login state before and after rotation.
type RefreshValidator func(context.Context, Principal) error

// KickoutStrategy determines which conflicting sessions are revoked.
type KickoutStrategy string

const (
	KickoutOldest KickoutStrategy = "oldest"
	KickoutLatest KickoutStrategy = "latest"
	KickoutAll    KickoutStrategy = "all"
)

// SessionPolicy limits sessions for a subject and its platforms.
type SessionPolicy struct {
	MaxSessions           int                       `json:"max_sessions" yaml:"max_sessions" mapstructure:"max_sessions"`
	DefaultPlatformPolicy PlatformPolicy            `json:"default_platform_policy" yaml:"default_platform_policy" mapstructure:"default_platform_policy"`
	PlatformPolicies      map[string]PlatformPolicy `json:"platform_policies" yaml:"platform_policies" mapstructure:"platform_policies"`
}

// PlatformPolicy controls whether a platform can log in and how conflicts are handled.
type PlatformPolicy struct {
	Enabled         bool            `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	MaxSessions     int             `json:"max_sessions" yaml:"max_sessions" mapstructure:"max_sessions"`
	RequireDeviceID bool            `json:"require_device_id" yaml:"require_device_id" mapstructure:"require_device_id"`
	KickoutStrategy KickoutStrategy `json:"kickout_strategy" yaml:"kickout_strategy" mapstructure:"kickout_strategy"`
	ExclusiveWith   []string        `json:"exclusive_with" yaml:"exclusive_with" mapstructure:"exclusive_with"`
}
