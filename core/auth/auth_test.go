package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNewRequiresPositiveMaxSessionTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	_, err := New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewRejectsSubsecondTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cases := []struct {
		name string
		set  func(*Config)
	}{
		{name: "access", set: func(cfg *Config) { cfg.AccessTTL = 500 * time.Millisecond }},
		{name: "refresh", set: func(cfg *Config) { cfg.RefreshTTL = 500 * time.Millisecond }},
		{name: "session", set: func(cfg *Config) { cfg.MaxSessionTTL = 500 * time.Millisecond }},
		{name: "compensation", set: func(cfg *Config) { cfg.RefreshCompensationTimeout = 500 * time.Millisecond }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Issuer = "aurora-test"
			cfg.Audience = []string{"aurora-client"}
			cfg.MaxSessionTTL = 2 * time.Hour
			tc.set(&cfg)
			_, err := New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestNewRejectsExcessiveRefreshCompensationTimeout(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.MaxSessionTTL = 2 * time.Hour
	cfg.RefreshCompensationTimeout = time.Minute + time.Nanosecond
	_, err := New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestKeyPrefixCanBeConfigured(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.KeyPrefix = "my-service:auth:{sessions}"
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.MaxSessionTTL = 2 * time.Hour
	m, err := New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)
	require.Equal(t, cfg.KeyPrefix, m.prefix)

	override := "override:auth:{sessions}"
	m, err = New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)), WithKeyPrefix(override))
	require.NoError(t, err)
	require.Equal(t, override, m.prefix)
}

func TestKeyPrefixMustContainHashTag(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.KeyPrefix = "invalid-prefix"
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.MaxSessionTTL = 2 * time.Hour
	_, err := New(rdb, cfg, WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewUsesSigningSecretFromConfig(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.SigningSecret = string(bytes.Repeat([]byte("c"), 32))
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.MaxSessionTTL = 2 * time.Hour
	m, err := New(rdb, cfg)
	require.NoError(t, err)
	pair, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "config-secret"}})
	require.NoError(t, err)
	_, err = m.VerifyAccessToken(pair.AccessToken)
	require.NoError(t, err)
}

func TestIssueRefreshRevokeLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	principal := Principal{Subject: "user-1", Type: "member", TenantID: "tenant-1", Provider: "local"}

	pair, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)
	claims, err := m.VerifyAccess(context.Background(), pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, principal.Subject, claims.Subject)

	refreshed, err := m.Refresh(context.Background(), pair.RefreshToken, nil)
	require.NoError(t, err)
	_, err = m.Refresh(context.Background(), pair.RefreshToken, nil)
	require.ErrorIs(t, err, ErrRefreshTokenReused)
	_, err = m.VerifyAccess(context.Background(), refreshed.AccessToken)
	require.NoError(t, err)

	require.NoError(t, m.RevokeSession(context.Background(), claims.SessionID))
	_, err = m.VerifyAccess(context.Background(), refreshed.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestIssueIdempotencyReplaysTokenPair(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	request := IssueRequest{Principal: Principal{Subject: "idem-user"}, Platform: "web", IdempotencyKey: "login-attempt-1"}
	first, err := m.Issue(context.Background(), request)
	require.NoError(t, err)
	second, err := m.Issue(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestIssueIdempotencyConcurrentRequestsReplayOnePair(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	request := IssueRequest{Principal: Principal{Subject: "idem-concurrent"}, Platform: "web", IdempotencyKey: "login-attempt-1"}
	results := make(chan *TokenPair, 8)
	errs := make(chan error, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			pair, err := m.Issue(context.Background(), request)
			results <- pair
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	var first *TokenPair
	for pair := range results {
		if first == nil {
			first = pair
			continue
		}
		require.Equal(t, first, pair)
	}
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestIssueIdempotencyRejectsDifferentRequest(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	_, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "idem-user"}, Platform: "web", IdempotencyKey: "login-attempt-1"})
	require.NoError(t, err)
	_, err = m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "idem-user"}, Platform: "web", AuthVersion: 1, IdempotencyKey: "login-attempt-1"})
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestIssueRejectsOversizedIdempotencyKey(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	_, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "idem-user"}, IdempotencyKey: strings.Repeat("x", 257)})
	require.ErrorIs(t, err, ErrInvalidIdempotencyKey)
}

func TestRevokeSubjectPageUsesConfiguredBatch(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	m.cfg.RevokeBatchSize = 2
	principal := Principal{Subject: "user-page"}
	tokens := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		pair, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
		require.NoError(t, err)
		tokens = append(tokens, pair.AccessToken)
	}

	total := 0
	for {
		page, err := m.RevokeSubjectPage(context.Background(), principal, 2)
		require.NoError(t, err)
		total += page.Revoked
		if !page.HasMore {
			break
		}
	}
	require.Equal(t, 5, total)
	for _, token := range tokens {
		_, err := m.VerifyAccess(context.Background(), token)
		require.ErrorIs(t, err, ErrSessionNotFound)
	}
}

func TestIssueEnforcesSubjectSessionLimit(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	m.cfg.Policy.MaxSessions = 1
	principal := Principal{Subject: "limited"}
	first, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)
	second, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)
	_, err = m.VerifyAccess(context.Background(), first.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = m.VerifyAccess(context.Background(), second.AccessToken)
	require.NoError(t, err)
}

func TestPlatformPolicyConflictAndForceLogin(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	m.cfg.Policy.PlatformPolicies = map[string]PlatformPolicy{
		"web": {
			Enabled:         true,
			MaxSessions:     1,
			KickoutStrategy: KickoutLatest,
		},
	}
	principal := Principal{Subject: "platform-limit"}
	first, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)
	_, err = m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.ErrorIs(t, err, ErrLoginConflict)
	second, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web", ForceLogin: true})
	require.NoError(t, err)
	_, err = m.VerifyAccess(context.Background(), first.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = m.VerifyAccess(context.Background(), second.AccessToken)
	require.NoError(t, err)
}

func TestExclusivePlatformRevokesTheConfiguredPlatform(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	m.cfg.Policy.MaxSessions = 3
	m.cfg.Policy.PlatformPolicies = map[string]PlatformPolicy{
		"web": {
			Enabled:         true,
			KickoutStrategy: KickoutOldest,
			ExclusiveWith:   []string{"mobile"},
		},
	}
	principal := Principal{Subject: "exclusive"}
	desktop, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "desktop"})
	require.NoError(t, err)
	mobile, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "mobile"})
	require.NoError(t, err)
	web, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)

	_, err = m.VerifyAccess(context.Background(), desktop.AccessToken)
	require.NoError(t, err)
	_, err = m.VerifyAccess(context.Background(), mobile.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = m.VerifyAccess(context.Background(), web.AccessToken)
	require.NoError(t, err)
}

func TestRefreshRepairsSessionIndexes(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	principal := Principal{Subject: "repair-index"}
	pair, err := m.Issue(context.Background(), IssueRequest{Principal: principal, Platform: "web"})
	require.NoError(t, err)
	principalKey := makePrincipalKey(principal)
	_, err = rdb.Del(context.Background(), m.subjectIndexKey(principalKey), m.platformIndexKey(principalKey, "web")).Result()
	require.NoError(t, err)

	refreshed, err := m.Refresh(context.Background(), pair.RefreshToken, nil)
	require.NoError(t, err)
	page, err := m.RevokeSubjectPage(context.Background(), principal, 1)
	require.NoError(t, err)
	require.Equal(t, 1, page.Revoked)
	require.False(t, page.HasMore)
	_, err = m.VerifyAccess(context.Background(), refreshed.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestRevokeSessionCleansOrphanWithoutRefreshKey(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	pair, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "orphan"}, Platform: "web"})
	require.NoError(t, err)
	claims, err := m.VerifyAccessToken(pair.AccessToken)
	require.NoError(t, err)
	_, err = rdb.Del(context.Background(), m.refreshKey(claims.SessionID)).Result()
	require.NoError(t, err)

	require.NoError(t, m.RevokeSession(context.Background(), claims.SessionID))
	_, err = m.VerifyAccess(context.Background(), pair.AccessToken)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestConcurrentRefreshOnlyOneSucceeds(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	pair, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "concurrent"}, Platform: "web"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Refresh(context.Background(), pair.RefreshToken, nil)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, reused int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRefreshTokenReused) {
			reused++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, reused)
}

func TestRefreshCannotPassAbsoluteSessionExpiry(t *testing.T) {
	base := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	clockNow := base
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.AccessTTL = 10 * time.Minute
	cfg.RefreshTTL = 30 * time.Minute
	cfg.MaxSessionTTL = time.Hour
	m, err := New(rdb, cfg,
		WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		WithClock(func() time.Time { return clockNow }),
	)
	require.NoError(t, err)

	pair, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "absolute"}, Platform: "web"})
	require.NoError(t, err)
	clockNow = base.Add(20 * time.Minute)
	firstRefresh, err := m.Refresh(context.Background(), pair.RefreshToken, nil)
	require.NoError(t, err)
	require.Equal(t, base.Add(50*time.Minute), firstRefresh.RefreshExpiresAt)
	clockNow = base.Add(40 * time.Minute)
	secondRefresh, err := m.Refresh(context.Background(), firstRefresh.RefreshToken, nil)
	require.NoError(t, err)
	require.Equal(t, base.Add(time.Hour), secondRefresh.RefreshExpiresAt)
	clockNow = base.Add(61 * time.Minute)
	_, err = m.Refresh(context.Background(), secondRefresh.RefreshToken, nil)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestRefreshValidatorFailureRevokesRotatedSession(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	m, rdb := newTestManager(t, now)
	defer rdb.Close()
	pair, err := m.Issue(context.Background(), IssueRequest{Principal: Principal{Subject: "validator"}, Platform: "web"})
	require.NoError(t, err)
	validationCalls := 0
	validationErr := errors.New("account disabled")
	_, err = m.Refresh(context.Background(), pair.RefreshToken, func(context.Context, Principal) error {
		validationCalls++
		if validationCalls == 2 {
			return validationErr
		}
		return nil
	})
	require.ErrorIs(t, err, ErrRefreshValidation)
	require.ErrorIs(t, err, validationErr)
	_, err = m.Refresh(context.Background(), pair.RefreshToken, nil)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func newTestManager(t *testing.T, now time.Time) (*Manager, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Issuer = "aurora-test"
	cfg.Audience = []string{"aurora-client"}
	cfg.MaxSessionTTL = 2 * time.Hour
	m, err := New(rdb, cfg,
		WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	return m, rdb
}
