package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
)

const defaultKeyPrefix = "aurora:auth:{auth}"

// Option 在构造 Manager 时配置选项。
type Option func(*managerOptions) error

type managerOptions struct {
	secret []byte
	prefix string
	now    func() time.Time
}

// WithSigningSecret 提供用于 token 签名与 refresh hash 的 HMAC 密钥。
func WithSigningSecret(secret []byte) Option {
	return func(options *managerOptions) error {
		if len(secret) == 0 {
			return ErrSigningKeyRequired
		}
		options.secret = append(options.secret[:0], secret...)
		return nil
	}
}

// WithKeyPrefix 设置 Redis key 前缀。前缀必须包含一个非空的 hash tag。
func WithKeyPrefix(prefix string) Option {
	return func(options *managerOptions) error {
		options.prefix = strings.TrimSpace(prefix)
		return nil
	}
}

// WithClock 注入时钟，主要用于可重复的单元测试和受控运行时。
func WithClock(now func() time.Time) Option {
	return func(options *managerOptions) error {
		if now == nil {
			return fmt.Errorf("%w: clock is nil", ErrInvalidConfig)
		}
		options.now = now
		return nil
	}
}

type Manager struct {
	cfg    Config
	rdb    redis.UniversalClient
	secret []byte
	prefix string
	now    func() time.Time
}

// New 使用应用持有的 Redis 客户端创建 auth Manager。
// Manager 不会关闭注入的客户端。
func New(rdb redis.UniversalClient, cfg Config, opts ...Option) (*Manager, error) {
	if isNilUniversalClient(rdb) {
		return nil, ErrRedisRequired
	}

	cfg = cfg.withDefaults()
	options := managerOptions{
		secret: []byte(cfg.SigningSecret),
		prefix: cfg.KeyPrefix,
		now:    time.Now,
	}
	for _, option := range opts {
		if option == nil {
			continue
		}
		if err := option(&options); err != nil {
			return nil, err
		}
	}
	if len(options.secret) < 32 {
		return nil, fmt.Errorf("%w: signing key must be at least 32 bytes", ErrSigningKeyRequired)
	}
	if err := validateKeyPrefix(options.prefix); err != nil {
		return nil, err
	}
	cfg.KeyPrefix = options.prefix
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	cfg.Audience = append([]string(nil), cfg.Audience...)
	cfg.Policy = cloneSessionPolicy(cfg.Policy)
	for i := range cfg.Audience {
		cfg.Audience[i] = strings.TrimSpace(cfg.Audience[i])
	}

	return &Manager{
		cfg:    cfg,
		rdb:    rdb,
		secret: append([]byte(nil), options.secret...),
		prefix: options.prefix,
		now:    options.now,
	}, nil
}

func isNilUniversalClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Issue 为指定 Principal 签发一对新的 access / refresh token。
// 流程：参数校验 → 平台策略检查 → 生成 sessionID 和 token → 通过 Lua 脚本
// 在 Redis 中原子完成会话登记、并发上限踢出以及幂等键写入。
// 幂等键相同时在 TTL 内直接返回缓存的 token pair（AES-GCM 加密存储）。
func (m *Manager) Issue(ctx context.Context, request IssueRequest) (*TokenPair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	principal, err := normalizePrincipal(request.Principal)
	if err != nil {
		return nil, err
	}
	platform := normalizePlatform(request.Platform)
	if err := validateKeySegment(platform, maxPlatformFieldLen, "platform"); err != nil {
		return nil, err
	}
	if err := validateOptionalKeySegment(request.DeviceID, maxPlatformFieldLen, "device id"); err != nil {
		return nil, err
	}
	if request.AuthVersion < 0 {
		return nil, fmt.Errorf("%w: auth version cannot be negative", ErrInvalidPrincipal)
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}

	policy := m.cfg.Policy.forPlatform(platform)
	if !policy.Enabled {
		return nil, ErrPlatformDisabled
	}
	if policy.RequireDeviceID && strings.TrimSpace(request.DeviceID) == "" {
		return nil, ErrDeviceRequired
	}
	deviceID := strings.TrimSpace(request.DeviceID)

	now := m.currentTime()
	abs := now.Add(m.cfg.MaxSessionTTL).Truncate(time.Second)
	expires := minTime(now.Add(m.cfg.RefreshTTL), abs).Truncate(time.Second)
	accessExpires := minTime(now.Add(m.cfg.AccessTTL), expires).Truncate(time.Second)
	if !expires.After(now) || !accessExpires.After(now) {
		return nil, ErrSessionExpired
	}

	sessionID := uuid.NewString()
	principalKey := makePrincipalKey(principal)
	accessJTI := uuid.NewString()
	refreshJTI := uuid.NewString()
	accessToken, err := m.signToken(principal, sessionID, platform, request.AuthVersion, accessJTI, TokenUseAccess, now, accessExpires)
	if err != nil {
		return nil, err
	}
	refreshToken, err := m.signToken(principal, sessionID, platform, request.AuthVersion, refreshJTI, TokenUseRefresh, now, expires)
	if err != nil {
		return nil, err
	}
	principalJSON, err := json.Marshal(principal)
	if err != nil {
		return nil, fmt.Errorf("%w: principal serialization: %v", ErrInvalidPrincipal, err)
	}
	var idempotencyFingerprint, idempotencyPayload string
	if idempotencyKey != "" {
		idempotencyFingerprint = issueFingerprint(principalJSON, request.AuthVersion, platform, deviceID, request.ForceLogin)
		pair := &TokenPair{TokenType: "Bearer", AccessToken: accessToken, RefreshToken: refreshToken, AccessExpiresAt: accessExpires, RefreshExpiresAt: expires}
		idempotencyPayload, err = m.encryptTokenPair(pair)
		if err != nil {
			return nil, fmt.Errorf("%w: idempotency payload: %v", ErrStoreUnavailable, err)
		}
	}

	result, err := issueScript.Run(ctx, m.rdb, []string{
		m.sessionKey(sessionID),
		m.refreshKey(sessionID),
		m.subjectIndexKey(principalKey),
		m.platformIndexKey(principalKey, platform),
		m.idempotencyKey(principalKey, platform, deviceID, idempotencyKey),
	}, issueScriptArgs(issueScriptInput{
		SessionID:              sessionID,
		RefreshHash:            m.hashRefresh(refreshToken),
		PrincipalJSON:          string(principalJSON),
		SubjectKey:             principalKey,
		AuthVersion:            request.AuthVersion,
		Platform:               platform,
		DeviceID:               deviceID,
		IssuedAt:               now.Unix(),
		ExpiresAt:              expires.Unix(),
		AbsoluteExpiresAt:      abs.Unix(),
		TTL:                    ttlSeconds(now, expires),
		MaxSessions:            m.cfg.Policy.MaxSessions,
		PlatformMax:            policy.MaxSessions,
		Strategy:               policy.KickoutStrategy,
		ForceLogin:             request.ForceLogin,
		ExclusiveWith:          policy.ExclusiveWith,
		Prefix:                 m.prefix,
		IdempotencyFingerprint: idempotencyFingerprint,
		IdempotencyPayload:     idempotencyPayload,
		IdempotencyTTL:         ttlSeconds(now, now.Add(m.cfg.IdempotencyTTL)),
	})).Result()
	if err != nil {
		return nil, storeError(err)
	}
	status := scriptStatus(result)
	if status == "IDEMPOTENCY_CONFLICT" {
		return nil, ErrIdempotencyConflict
	}
	if status == "IDEMPOTENCY_REPLAY" {
		items, ok := result.([]interface{})
		if !ok || len(items) < 2 {
			return nil, fmt.Errorf("%w: invalid idempotency replay result", ErrStoreUnavailable)
		}
		payload, ok := items[1].(string)
		if !ok {
			if raw, okBytes := items[1].([]byte); okBytes {
				payload, ok = string(raw), true
			}
		}
		if !ok {
			return nil, fmt.Errorf("%w: invalid idempotency replay payload", ErrStoreUnavailable)
		}
		pair, err := m.decryptTokenPair(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid idempotency replay payload", ErrStoreUnavailable)
		}
		return pair, nil
	}
	if status == "CONFLICT" {
		return nil, ErrLoginConflict
	}
	if scriptStatus(result) != "OK" {
		return nil, fmt.Errorf("%w: issue script returned %q", ErrStoreUnavailable, scriptStatus(result))
	}

	return &TokenPair{
		TokenType:        "Bearer",
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpires,
		RefreshExpiresAt: expires,
	}, nil
}

func validateIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 256 || strings.IndexByte(key, 0) >= 0 {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

// issueFingerprint 计算幂等请求指纹，用于识别"同 Principal、同参数"的重复登录。
func issueFingerprint(principalJSON []byte, authVersion int64, platform, deviceID string, forceLogin bool) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%t", principalJSON, authVersion, platform, deviceID, forceLogin)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// idempotencyKey 生成幂等键对应的 Redis key，使用 SHA-256 摘要避免泄露原始参数。
func (m *Manager) idempotencyKey(principalKey, platform, deviceID, key string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", principalKey, platform, deviceID, key)
	return m.prefix + ":idempotency:" + fmt.Sprintf("%x", h.Sum(nil))
}

// idempotencyCipher 派生 AES-GCM 密钥，用于加密缓存的 token pair，防止 Redis 数据泄露导致 token 直接泄露。
func (m *Manager) idempotencyCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("aurora-auth-idempotency-v1\x00"), m.secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (m *Manager) encryptTokenPair(pair *TokenPair) (string, error) {
	aead, err := m.idempotencyCipher()
	if err != nil {
		return "", err
	}
	plaintext, err := json.Marshal(pair)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (m *Manager) decryptTokenPair(payload string) (*TokenPair, error) {
	aead, err := m.idempotencyCipher()
	if err != nil {
		return nil, err
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, ErrTokenMalformed
	}
	pairBytes, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return nil, err
	}
	var pair TokenPair
	if err := json.Unmarshal(pairBytes, &pair); err != nil {
		return nil, err
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		return nil, ErrTokenMalformed
	}
	return &pair, nil
}

// VerifyAccessToken 仅校验 access token 的签名、issuer、audience 和过期时间，
// 不查询 Redis。适合性能敏感、允许一定过期窗口的场景。
func (m *Manager) VerifyAccessToken(raw string) (*Claims, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrTokenRequired
	}
	return m.parseToken(raw, TokenUseAccess)
}

// VerifyAccess 在 VerifyAccessToken 的基础上，进一步加载 Redis 中的会话
// 并检查撤销、过期、AuthVersion 与平台一致性。是默认推荐的安全校验入口。
func (m *Manager) VerifyAccess(ctx context.Context, raw string) (*Claims, error) {
	claims, err := m.VerifyAccessToken(raw)
	if err != nil {
		return nil, err
	}
	session, err := m.loadSession(ctx, claims.SessionID)
	if err != nil {
		return nil, err
	}
	now := m.currentTime()
	if !session.RevokedAt.IsZero() {
		return nil, ErrSessionRevoked
	}
	if !session.ExpiresAt.After(now) {
		return nil, ErrSessionExpired
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Time.After(session.ExpiresAt) {
		return nil, ErrSessionMismatch
	}
	if !principalMatchesClaims(session.Principal, claims) || session.AuthVersion != claims.AuthVersion || session.Platform != claims.Platform {
		return nil, ErrSessionMismatch
	}
	return claims, nil
}

// Refresh 使用 refresh token 轮换出新的 token pair。
// 关键安全机制：
//  1. Lua 脚本原子比较旧 hash 并写入新 hash，防止同一 token 被并发重复使用；
//  2. 轮换前调用 validator 校验业务登录态；
//  3. 轮换后再次调用 validator，失败时通过 RevokeSession 撤销刚创建的会话
//     （使用 WithoutCancel + 超时，避免请求取消导致脏会话残留）。
func (m *Manager) Refresh(ctx context.Context, raw string, validator RefreshValidator) (*TokenPair, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrTokenRequired
	}
	claims, err := m.parseToken(raw, TokenUseRefresh)
	if err != nil {
		return nil, err
	}
	session, err := m.loadSession(ctx, claims.SessionID)
	if err != nil {
		return nil, err
	}
	now := m.currentTime()
	if !session.ExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) {
		return nil, ErrSessionExpired
	}
	if !principalMatchesClaims(session.Principal, claims) || session.AuthVersion != claims.AuthVersion || session.Platform != claims.Platform {
		return nil, ErrSessionMismatch
	}
	if session.CurrentRefreshHash != m.hashRefresh(raw) {
		return nil, ErrRefreshTokenReused
	}
	if validator != nil {
		if err := validator(ctx, session.Principal); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRefreshValidation, err)
		}
	}

	expires := minTime(now.Add(m.cfg.RefreshTTL), session.AbsoluteExpiresAt).Truncate(time.Second)
	accessExpires := minTime(now.Add(m.cfg.AccessTTL), expires).Truncate(time.Second)
	if !expires.After(now) || !accessExpires.After(now) {
		return nil, ErrSessionExpired
	}
	accessToken, err := m.signToken(session.Principal, session.ID, session.Platform, session.AuthVersion, uuid.NewString(), TokenUseAccess, now, accessExpires)
	if err != nil {
		return nil, err
	}
	refreshToken, err := m.signToken(session.Principal, session.ID, session.Platform, session.AuthVersion, uuid.NewString(), TokenUseRefresh, now, expires)
	if err != nil {
		return nil, err
	}
	result, err := refreshScript.Run(ctx, m.rdb, []string{m.sessionKey(session.ID), m.refreshKey(session.ID)}, refreshScriptArgs(refreshScriptInput{
		ExpectedHash:  session.CurrentRefreshHash,
		NewHash:       m.hashRefresh(refreshToken),
		Now:           now.Unix(),
		ExpiresAt:     expires.Unix(),
		LastRefreshed: now.Unix(),
		TTL:           ttlSeconds(now, expires),
		Prefix:        m.prefix,
		SubjectKey:    makePrincipalKey(session.Principal),
		Platform:      session.Platform,
		DeviceID:      session.DeviceID,
		SessionID:     session.ID,
	})).Result()
	if err != nil {
		return nil, storeError(err)
	}
	switch scriptStatus(result) {
	case "NOT_FOUND":
		return nil, ErrSessionNotFound
	case "EXPIRED":
		return nil, ErrSessionExpired
	case "REUSED":
		return nil, ErrRefreshTokenReused
	case "OK":
	default:
		return nil, fmt.Errorf("%w: refresh script returned %q", ErrStoreUnavailable, scriptStatus(result))
	}
	if validator != nil {
		if err := validator(ctx, session.Principal); err != nil {
			// 轮换已经提交，无法回滚。使用 WithoutCancel + 独立超时做补偿撤销，
			// 避免请求被客户端取消后脏会话继续存活；超时上限防止 Redis 故障
			// 把请求挂死。
			compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.RefreshCompensationTimeout)
			defer cancel()
			if revokeErr := m.RevokeSession(compensationCtx, session.ID); revokeErr != nil {
				return nil, fmt.Errorf("%w: %w; revoke session: %w", ErrRefreshValidation, err, revokeErr)
			}
			return nil, fmt.Errorf("%w: %w", ErrRefreshValidation, err)
		}
	}
	return &TokenPair{
		TokenType:        "Bearer",
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpires,
		RefreshExpiresAt: expires,
	}, nil
}

// RevokeSession 撤销单个会话，同时清理其在 subject / platform / device 索引中的成员。
func (m *Manager) RevokeSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrSessionNotFound
	}
	session, err := m.loadSessionMetadata(ctx, sessionID)
	if err != nil {
		return err
	}
	principalKey := makePrincipalKey(session.Principal)
	result, err := revokeSessionScript.Run(ctx, m.rdb, []string{
		m.sessionKey(sessionID),
		m.refreshKey(sessionID),
		m.subjectIndexKey(principalKey),
		m.platformIndexKey(principalKey, session.Platform),
		m.deviceIndexKey(principalKey, session.DeviceID),
	}, []interface{}{m.prefix, principalKey, session.Platform, session.DeviceID}).Result()
	if err != nil {
		return storeError(err)
	}
	if scriptStatus(result) == "0" {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeSubject 撤销指定主体的所有会话。内部按 RevokeBatchSize 分页，
// 直到主体索引被清空，避免单次操作阻塞 Redis。
func (m *Manager) RevokeSubject(ctx context.Context, principal Principal) error {
	for {
		page, err := m.RevokeSubjectPage(ctx, principal, m.cfg.RevokeBatchSize)
		if err != nil {
			return err
		}
		if !page.HasMore {
			return nil
		}
	}
}

// RevokeSubjectPage 撤销指定主体最多 limit 个会话，返回是否还有剩余。
// 由 RevokeSubject 循环调用，也可单独使用实现"撤销 N 个最老会话"的策略。
func (m *Manager) RevokeSubjectPage(ctx context.Context, principal Principal, limit int) (RevokePage, error) {
	principal, err := normalizePrincipal(principal)
	if err != nil {
		return RevokePage{}, err
	}
	if limit <= 0 || limit > m.cfg.RevokeBatchSize {
		return RevokePage{}, ErrInvalidBatchSize
	}
	principalKey := makePrincipalKey(principal)
	result, err := revokeSubjectPageScript.Run(ctx, m.rdb, []string{m.subjectIndexKey(principalKey)}, []interface{}{
		m.prefix,
		principalKey,
		strconvItoa(limit),
	}).Result()
	if err != nil {
		return RevokePage{}, storeError(err)
	}
	values, ok := scriptValues(result)
	if !ok || len(values) < 2 {
		return RevokePage{}, fmt.Errorf("%w: invalid revoke page result", ErrStoreUnavailable)
	}
	return RevokePage{Revoked: int(values[0]), HasMore: values[1] != 0}, nil
}

// normalizePrincipal 修剪字段并做基本合法性校验（非空 subject、长度与 NUL 字符检查）。
func normalizePrincipal(principal Principal) (Principal, error) {
	principal.Subject = strings.TrimSpace(principal.Subject)
	principal.Type = strings.TrimSpace(principal.Type)
	principal.TenantID = strings.TrimSpace(principal.TenantID)
	principal.Provider = strings.TrimSpace(principal.Provider)
	if principal.Subject == "" {
		return Principal{}, ErrInvalidPrincipal
	}
	for name, value := range map[string]string{
		"subject": principal.Subject, "type": principal.Type, "tenant": principal.TenantID, "provider": principal.Provider,
	} {
		if len(value) > maxPrincipalFieldLen || strings.IndexByte(value, 0) >= 0 {
			return Principal{}, fmt.Errorf("%w: %s is invalid", ErrInvalidPrincipal, name)
		}
	}
	return principal, nil
}

// normalizePlatform 空平台归一化为 "default"，保证索引 key 稳定。
func normalizePlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return defaultPlatformName
	}
	return platform
}

// validateOptionalKeySegment 与 validateKeySegment 的区别是允许空值（空值会被上层跳过）。
func validateOptionalKeySegment(value string, max int, name string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateKeySegment(value, max, name)
}

// validateKeySegment 校验用于 Redis key 的字段：非空、长度受限、只允许
// 字母、数字、'-'、'_'、'.'，避免 key 注入与解析歧义。
func validateKeySegment(value string, max int, name string) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%w: %s is empty or too long", ErrInvalidConfig, name)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: %s contains unsupported characters", ErrInvalidConfig, name)
	}
	return nil
}

// validateKeyPrefix 校验 Redis key 前缀：
//   - 不能包含空白字符（防止 RESP 协议注入）；
//   - 必须包含且只包含一对完整的 {} hash tag，保证 cluster 模式下相关 key
//     落到同一 slot，Lua 脚本才能原子访问。
func validateKeyPrefix(prefix string) error {
	if prefix == "" || strings.ContainsAny(prefix, " \t\r\n") {
		return fmt.Errorf("%w: invalid key prefix", ErrInvalidConfig)
	}
	open := strings.IndexByte(prefix, '{')
	if open < 0 {
		return fmt.Errorf("%w: key prefix must contain a redis hash tag", ErrInvalidConfig)
	}
	closeOffset := strings.IndexByte(prefix[open+1:], '}')
	if closeOffset <= 0 || strings.IndexByte(prefix[open+1:open+1+closeOffset], '{') >= 0 || strings.IndexByte(prefix[open+1+closeOffset+1:], '}') >= 0 {
		return fmt.Errorf("%w: invalid redis hash tag in key prefix", ErrInvalidConfig)
	}
	return nil
}

// makePrincipalKey 将 Principal 编码为稳定的索引 key 段，
// 使用 \x00 分隔避免字段拼接歧义，再做 base64 URL 安全编码。
func makePrincipalKey(principal Principal) string {
	value := principal.Type + "\x00" + principal.TenantID + "\x00" + principal.Subject + "\x00" + principal.Provider
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// principalMatchesClaims 校验 Redis 会话中的 Principal 与 JWT claims 是否一致。
// 任何一个字段漂移都说明 token 与会话已脱钩，应视为会话不匹配。
func principalMatchesClaims(principal Principal, claims *Claims) bool {
	return principal.Subject == claims.Subject && principal.Type == claims.PrincipalType && principal.TenantID == claims.TenantID && principal.Provider == claims.Provider
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func ttlSeconds(now, expires time.Time) int64 {
	ttl := int64(expires.Sub(now) / time.Second)
	if ttl < 1 {
		return 1
	}
	return ttl
}

// currentTime 返回 UTC 秒级时间，保证存储到 Redis 与签入 JWT 的时间精度一致。
func (m *Manager) currentTime() time.Time {
	return m.now().UTC().Truncate(time.Second)
}

// hashRefresh 使用 HMAC-SHA256 计算 refresh token 的存储散列，
// 让 Redis 中只保存散列而非原始 token，降低数据泄露风险。
func (m *Manager) hashRefresh(token string) string {
	h := hmac.New(sha256.New, m.secret)
	_, _ = h.Write([]byte(token))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func strconvItoa(value int) string {
	return fmt.Sprintf("%d", value)
}
