package auth

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Principal 标识一个已认证的主体，与具体用户表解耦。
type Principal struct {
	Subject  string `json:"subject"`
	Type     string `json:"type,omitempty"`
	TenantID string `json:"tenant_id,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Claims 是 access 与 refresh token 共享的声明集合。
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

// Session 是一次登录在 Redis 中保存的状态。
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

// IssueRequest 描述发起新会话所需的身份与客户端元信息。
type IssueRequest struct {
	Principal   Principal
	AuthVersion int64
	Platform    string
	DeviceID    string
	ForceLogin  bool
	// IdempotencyKey 标识一次逻辑登录尝试。在配置的 TTL 内重复使用
	// 相同 IdempotencyKey 与相同请求参数会返回原始的 token pair。
	IdempotencyKey string
}

// RevokePage 表示一次受限的按主体撤销操作的结果。
type RevokePage struct {
	Revoked int
	HasMore bool
}

// RefreshValidator 在 token 轮换前后回调业务层校验登录状态。
type RefreshValidator func(context.Context, Principal) error

// KickoutStrategy 决定冲突会话的踢出方式。
type KickoutStrategy string

const (
	KickoutOldest KickoutStrategy = "oldest"
	KickoutLatest KickoutStrategy = "latest"
	KickoutAll    KickoutStrategy = "all"
)

// SessionPolicy 限定单个主体在不同平台上的会话数量。
type SessionPolicy struct {
	MaxSessions           int                       `json:"max_sessions" yaml:"max_sessions" mapstructure:"max_sessions"`
	DefaultPlatformPolicy PlatformPolicy            `json:"default_platform_policy" yaml:"default_platform_policy" mapstructure:"default_platform_policy"`
	PlatformPolicies      map[string]PlatformPolicy `json:"platform_policies" yaml:"platform_policies" mapstructure:"platform_policies"`
}

// PlatformPolicy 控制某平台是否允许登录，以及冲突时如何处理。
type PlatformPolicy struct {
	Enabled         bool            `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	MaxSessions     int             `json:"max_sessions" yaml:"max_sessions" mapstructure:"max_sessions"`
	RequireDeviceID bool            `json:"require_device_id" yaml:"require_device_id" mapstructure:"require_device_id"`
	KickoutStrategy KickoutStrategy `json:"kickout_strategy" yaml:"kickout_strategy" mapstructure:"kickout_strategy"`
	ExclusiveWith   []string        `json:"exclusive_with" yaml:"exclusive_with" mapstructure:"exclusive_with"`
}
