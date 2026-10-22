package aurora

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"github.com/surge-go/aurora/core/auth"
)

func writeLifecycleAuthConfig(t *testing.T, addr, authYAML string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := "aurora:\n  addr: ':0'\n  mode: test\n"
	if addr != "" {
		config += fmt.Sprintf("redis:\n  mode: standalone\n  addrs: [%q]\n", addr)
	}
	require.NoError(t, os.WriteFile(path, []byte(config+authYAML), 0o600))
	return path
}

func namedAuthConfig(name, secret, issuer, audience, extra string) string {
	return fmt.Sprintf("  %s:\n    signing_secret: %q\n    issuer: %s\n    audience: [%s]\n    max_session_ttl: 2h\n%s", name, secret, issuer, audience, extra)
}

func TestLifecycleAuthLookup(t *testing.T) {
	config := "auth:\n" + namedAuthConfig("admin", strings.Repeat("a", 32), "admin", "admin-client", "") +
		namedAuthConfig("user", strings.Repeat("u", 32), "user", "user-client", "")
	lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, "127.0.0.1:6379", config))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
	admin, err := lifecycle.Auth("admin")
	require.NoError(t, err)
	user, err := lifecycle.Auth("user")
	require.NoError(t, err)
	require.NotSame(t, admin, user)
	names := lifecycle.AuthNames()
	require.Equal(t, []string{"admin", "user"}, names)
	names[0] = "changed"
	require.Equal(t, []string{"admin", "user"}, lifecycle.AuthNames())
	_, err = lifecycle.Auth("admin")
	require.NoError(t, err)
	for _, name := range []string{"", "missing", "Admin", " admin "} {
		manager, err := lifecycle.Auth(name)
		require.Nil(t, manager)
		require.ErrorIs(t, err, ErrAuthInstanceNotFound)
		require.Contains(t, err.Error(), fmt.Sprintf("%q", name))
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			manager, err := lifecycle.Auth("admin")
			if err != nil || manager != admin {
				t.Errorf("concurrent lookup = %p, %v; want %p", manager, err, admin)
			}
			names := lifecycle.AuthNames()
			if !slices.Equal(names, []string{"admin", "user"}) {
				t.Errorf("concurrent AuthNames() = %v", names)
			}
			names[0] = "changed"
		})
	}
	wg.Wait()
	var empty Lifecycle
	require.Nil(t, empty.AuthNames())
	_, err = empty.Auth("admin")
	require.ErrorIs(t, err, ErrAuthInstanceNotFound)
	var missing *Lifecycle
	require.Nil(t, missing.AuthNames())
	_, err = missing.Auth("admin")
	require.EqualError(t, err, "lifecycle is nil")
}

func TestLifecycleAuthEmptyConfig(t *testing.T) {
	for _, config := range []string{"", "auth: {}\n"} {
		t.Run(config, func(t *testing.T) {
			lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, "", config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
			_, err = lifecycle.Auth("admin")
			require.ErrorIs(t, err, ErrAuthInstanceNotFound)
			require.Nil(t, lifecycle.Redis)
			require.Nil(t, lifecycle.AuthNames())
		})
	}
}

func TestLifecycleAuthConfigurationNames(t *testing.T) {
	for _, name := range []string{"Admin", "a", "admin_2-web", strings.Repeat("a", 64)} {
		t.Run(name, func(t *testing.T) {
			config := "auth:\n" + namedAuthConfig(name, strings.Repeat("k", 32), "issuer", "client", "")
			lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, "127.0.0.1:6379", config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
			manager, err := lifecycle.Auth(strings.ToLower(name))
			require.NoError(t, err)
			require.NotNil(t, manager)
		})
	}
}

func TestLifecycleAuthGeneratedTemplate(t *testing.T) {
	_, block, found := strings.Cut(defaultLifecycleYAML, "#auth:\n")
	require.True(t, found)
	block, _, found = strings.Cut(block, "\n\n")
	require.True(t, found)
	config := "auth:\n"
	for line := range strings.SplitSeq(block, "\n") {
		require.True(t, strings.HasPrefix(line, "#"))
		config += strings.TrimPrefix(line, "#") + "\n"
	}
	mr := miniredis.RunT(t)
	lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, mr.Addr(), config))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
	for _, name := range []string{"admin", "user"} {
		manager, err := lifecycle.Auth(name)
		require.NoError(t, err)
		pair, err := manager.Issue(context.Background(), auth.IssueRequest{Principal: auth.Principal{Subject: "template-user"}})
		require.NoError(t, err)
		_, err = manager.VerifyAccess(context.Background(), pair.AccessToken)
		require.NoError(t, err)
	}
}

func TestNewLifecycleRejectsInvalidAuth(t *testing.T) {
	secret := strings.Repeat("secret", 6)
	valid := namedAuthConfig("admin", secret, "admin", "admin-client", "")
	cases := []struct {
		name, config, want string
	}{
		{"flat config", "auth:\n  signing_secret: " + secret + "\n", "non-empty configuration map"},
		{"scalar", "auth: " + secret + "\n", "map of named instances"},
		{"list", "auth: [admin, user]\n", "map of named instances"},
		{"null instance", "auth:\n  admin:\n", `initialize auth "admin"`},
		{"empty instance", "auth:\n  admin: {}\n", `initialize auth "admin"`},
		{"scalar instance", "auth:\n  admin: " + secret + "\n", `initialize auth "admin"`},
		{"invalid name", "auth:\n" + namedAuthConfig("bad!name", secret, "admin", "admin-client", ""), "name must match"},
		{"digit first", "auth:\n" + namedAuthConfig("1admin", secret, "admin", "admin-client", ""), "name must match"},
		{"long name", "auth:\n" + namedAuthConfig(strings.Repeat("a", 65), secret, "admin", "admin-client", ""), "name must match"},
		{"unknown field", "auth:\n" + valid + "    acess_ttl: 10m\n", "invalid configuration fields or types"},
		{"invalid duration", "auth:\n" + valid + "    access_ttl: " + secret + "\n", "invalid configuration fields or types"},
		{"wrong secret type", "auth:\n  admin:\n    signing_secret: [" + secret + "]\n", "invalid configuration fields or types"},
		{"invalid policy field", "auth:\n" + valid + "    policy:\n      max_sesions: 1\n", "invalid configuration fields or types"},
		{"missing secret", "auth:\n  admin:\n    issuer: admin\n    audience: [admin-client]\n    max_session_ttl: 2h\n", "signing key"},
		{"invalid prefix", "auth:\n" + valid + "    key_prefix: invalid\n", "redis hash tag"},
		{"duplicate prefix", "auth:\n" + valid + "    key_prefix: ' shared:{auth} '\n" + namedAuthConfig("user", secret, "user", "user-client", "    key_prefix: 'shared:{auth}'\n"), `key_prefix conflicts with instance "admin"`},
		{"generated prefix conflict", "auth:\n" + valid + namedAuthConfig("user", secret, "user", "user-client", "    key_prefix: 'aurora:auth:{admin}'\n"), `key_prefix conflicts with instance "admin"`},
		{"invalid second instance", "auth:\n" + valid + namedAuthConfig("user", "short", "user", "user-client", ""), `initialize auth "user"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, "127.0.0.1:6379", tc.config))
			require.Nil(t, lifecycle)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.NotContains(t, err.Error(), secret)
		})
	}
	t.Run("redis required", func(t *testing.T) {
		lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, "", "auth:\n"+valid))
		require.Nil(t, lifecycle)
		require.EqualError(t, err, "initialize auth: redis must be configured")
	})
}

func TestLifecycleAuthTokenValidationIsolation(t *testing.T) {
	for _, dimension := range []string{"secret", "issuer", "audience"} {
		t.Run(dimension, func(t *testing.T) {
			mr := miniredis.RunT(t)
			secret, issuer, audience := strings.Repeat("a", 32), "admin", "admin-client"
			userSecret, userIssuer, userAudience := secret, issuer, audience
			var want error
			switch dimension {
			case "secret":
				userSecret = strings.Repeat("u", 32)
				want = auth.ErrInvalidSignature
			case "issuer":
				userIssuer = "user"
				want = auth.ErrInvalidIssuer
			case "audience":
				userAudience = "user-client"
				want = auth.ErrInvalidAudience
			}
			config := "auth:\n" + namedAuthConfig("admin", secret, issuer, audience, "") +
				namedAuthConfig("user", userSecret, userIssuer, userAudience, "")
			lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, mr.Addr(), config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
			admin, err := lifecycle.Auth("admin")
			require.NoError(t, err)
			user, err := lifecycle.Auth("user")
			require.NoError(t, err)
			for _, managers := range [][2]*auth.Manager{{admin, user}, {user, admin}} {
				pair, err := managers[0].Issue(context.Background(), auth.IssueRequest{Principal: auth.Principal{Subject: "same-user"}})
				require.NoError(t, err)
				_, err = managers[0].VerifyAccess(context.Background(), pair.AccessToken)
				require.NoError(t, err)
				_, err = managers[1].VerifyAccessToken(pair.AccessToken)
				require.ErrorIs(t, err, want)
				_, err = managers[1].Refresh(context.Background(), pair.RefreshToken, nil)
				require.ErrorIs(t, err, want)
			}
		})
	}
}

func TestLifecycleAuthSessionIsolation(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom_prefix=%t", custom), func(t *testing.T) {
			mr := miniredis.RunT(t)
			ctx := context.Background()
			config := "auth:\n"
			prefixes := map[string]string{}
			for _, name := range []string{"admin", "user"} {
				prefixes[name] = "aurora:auth:{" + name + "}"
				extra := "    policy:\n      max_sessions: 1\n"
				if custom {
					prefixes[name] = "service:" + name + ":{sessions}"
					extra += fmt.Sprintf("    key_prefix: %q\n", prefixes[name])
				}
				// Identical JWT settings deliberately exercise storage isolation alone.
				config += namedAuthConfig(name, strings.Repeat("k", 32), "same-issuer", "same-client", extra)
			}
			lifecycle, err := NewLifecycle(writeLifecycleAuthConfig(t, mr.Addr(), config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(ctx)) })
			admin, err := lifecycle.Auth("admin")
			require.NoError(t, err)
			user, err := lifecycle.Auth("user")
			require.NoError(t, err)
			request := auth.IssueRequest{Principal: auth.Principal{Subject: "same-user"}, Platform: "web", IdempotencyKey: "same-login"}
			adminPair, err := admin.Issue(ctx, request)
			require.NoError(t, err)
			userPair, err := user.Issue(ctx, request)
			require.NoError(t, err)
			require.NotEqual(t, adminPair.AccessToken, userPair.AccessToken)
			adminClaims, err := admin.VerifyAccess(ctx, adminPair.AccessToken)
			require.NoError(t, err)
			userClaims, err := user.VerifyAccess(ctx, userPair.AccessToken)
			require.NoError(t, err)
			require.True(t, mr.Exists(prefixes["admin"]+":session:"+adminClaims.SessionID))
			require.True(t, mr.Exists(prefixes["user"]+":session:"+userClaims.SessionID))
			_, err = admin.VerifyAccess(ctx, userPair.AccessToken)
			require.ErrorIs(t, err, auth.ErrSessionNotFound)
			_, err = admin.Refresh(ctx, userPair.RefreshToken, nil)
			require.ErrorIs(t, err, auth.ErrSessionNotFound)
			request.IdempotencyKey = "second-login"
			_, err = admin.Issue(ctx, request)
			require.NoError(t, err)
			_, err = admin.VerifyAccess(ctx, adminPair.AccessToken)
			require.ErrorIs(t, err, auth.ErrSessionNotFound)
			require.NoError(t, admin.RevokeSubject(ctx, request.Principal))
			_, err = user.VerifyAccess(ctx, userPair.AccessToken)
			require.NoError(t, err)
			refreshed, err := user.Refresh(ctx, userPair.RefreshToken, nil)
			require.NoError(t, err)
			_, err = user.VerifyAccess(ctx, refreshed.AccessToken)
			require.NoError(t, err)
			require.NoError(t, lifecycle.Close(ctx))
			require.Error(t, lifecycle.Redis.Ping(ctx).Err())
			_, err = user.VerifyAccess(ctx, refreshed.AccessToken)
			require.ErrorIs(t, err, auth.ErrStoreUnavailable)
		})
	}
}
