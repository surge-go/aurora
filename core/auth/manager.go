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

// Option configures a Manager at construction time.
type Option func(*managerOptions) error

type managerOptions struct {
	secret []byte
	prefix string
	now    func() time.Time
}

// WithSigningSecret supplies the HMAC key used for both token signing and refresh hashing.
func WithSigningSecret(secret []byte) Option {
	return func(options *managerOptions) error {
		if len(secret) == 0 {
			return ErrSigningKeyRequired
		}
		options.secret = append(options.secret[:0], secret...)
		return nil
	}
}

// WithKeyPrefix sets the Redis key prefix. It must contain one non-empty hash tag.
func WithKeyPrefix(prefix string) Option {
	return func(options *managerOptions) error {
		options.prefix = strings.TrimSpace(prefix)
		return nil
	}
}

// WithClock is intended for deterministic tests and controlled runtimes.
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

// New creates an auth manager using an application-owned Redis client.
// The manager never closes the injected client.
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

func issueFingerprint(principalJSON []byte, authVersion int64, platform, deviceID string, forceLogin bool) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%t", principalJSON, authVersion, platform, deviceID, forceLogin)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (m *Manager) idempotencyKey(principalKey, platform, deviceID, key string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", principalKey, platform, deviceID, key)
	return m.prefix + ":idempotency:" + fmt.Sprintf("%x", h.Sum(nil))
}

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

func (m *Manager) VerifyAccessToken(raw string) (*Claims, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrTokenRequired
	}
	return m.parseToken(raw, TokenUseAccess)
}

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
			// Rotation has already committed. Keep the compensating revoke alive
			// even when the request context was cancelled by the validator.
			if revokeErr := m.RevokeSession(context.WithoutCancel(ctx), session.ID); revokeErr != nil {
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

func normalizePlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return defaultPlatformName
	}
	return platform
}

func validateOptionalKeySegment(value string, max int, name string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateKeySegment(value, max, name)
}

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

func makePrincipalKey(principal Principal) string {
	value := principal.Type + "\x00" + principal.TenantID + "\x00" + principal.Subject + "\x00" + principal.Provider
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

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

func (m *Manager) currentTime() time.Time {
	return m.now().UTC().Truncate(time.Second)
}

func (m *Manager) hashRefresh(token string) string {
	h := hmac.New(sha256.New, m.secret)
	_, _ = h.Write([]byte(token))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func strconvItoa(value int) string {
	return fmt.Sprintf("%d", value)
}
