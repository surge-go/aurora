package attempt

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
)

// Limiter stores account login-attempt state in Redis. It does not own or
// close the injected client.
type Limiter struct {
	rdb          redis.UniversalClient
	cfg          Config
	prefix       string
	hmacKey      []byte
	failureLimit int
	windowSec    int64
	lockSec      int64
	attemptTTL   int64
}

// New creates a Redis-backed login-attempt limiter.
func New(rdb redis.UniversalClient, cfg Config) (*Limiter, error) {
	if isNilUniversalClient(rdb) {
		return nil, ErrRedisRequired
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Limiter{
		rdb:          rdb,
		cfg:          cfg,
		prefix:       cfg.KeyPrefix,
		hmacKey:      []byte(cfg.HMACKey),
		failureLimit: cfg.FailureLimit,
		windowSec:    durationSeconds(cfg.Window),
		lockSec:      durationSeconds(cfg.LockDuration),
		attemptTTL:   durationSeconds(cfg.AttemptTTL),
	}, nil
}

// Begin checks whether an account may start a credential verification attempt.
// When the account is locked, it returns a locked Status and a zero Attempt.
// The returned Attempt must be settled exactly once with RecordFailure or
// RecordSuccess.
func (l *Limiter) Begin(ctx context.Context, accountKey string) (Attempt, Status, error) {
	hash, err := l.accountHash(accountKey)
	if err != nil {
		return Attempt{}, Status{}, err
	}
	for i := 0; i < 3; i++ {
		id := uuid.NewString()
		result, err := beginScript.Run(ctx, l.rdb, []string{l.stateKey(hash), l.attemptKey(hash, id)}, id, l.failureLimit, l.windowSec, l.lockSec, l.attemptTTL).Result()
		if err != nil {
			return Attempt{}, Status{}, storeError(err)
		}
		items, ok := resultStrings(result)
		if !ok || len(items) == 0 {
			return Attempt{}, Status{}, fmt.Errorf("%w: invalid begin result", ErrStoreUnavailable)
		}
		switch items[0] {
		case "LOCKED":
			status, ok := parseLockedStatus(items, 1)
			if !ok {
				return Attempt{}, Status{}, fmt.Errorf("%w: invalid locked result", ErrStoreUnavailable)
			}
			return Attempt{}, status, nil
		case "OK":
			if len(items) != 5 {
				return Attempt{}, Status{}, fmt.Errorf("%w: invalid begin result", ErrStoreUnavailable)
			}
			epoch, ok := parseInt64(items[2])
			if !ok || epoch <= 0 {
				return Attempt{}, Status{}, fmt.Errorf("%w: invalid attempt epoch", ErrStoreUnavailable)
			}
			failureCount, ok := parseInt(items[3])
			if !ok {
				return Attempt{}, Status{}, fmt.Errorf("%w: invalid failure count", ErrStoreUnavailable)
			}
			remaining, ok := parseInt(items[4])
			if !ok {
				return Attempt{}, Status{}, fmt.Errorf("%w: invalid remaining count", ErrStoreUnavailable)
			}
			return Attempt{id: items[1], accountHash: hash, epoch: epoch}, Status{FailureCount: failureCount, RemainingAttempt: remaining}, nil
		case "ATTEMPT_EXISTS":
			continue
		default:
			return Attempt{}, Status{}, fmt.Errorf("%w: unknown begin result %q", ErrStoreUnavailable, items[0])
		}
	}
	return Attempt{}, Status{}, fmt.Errorf("%w: could not allocate attempt", ErrStoreUnavailable)
}

// RecordFailure records one failed credential verification. A stale handle
// (including one invalidated by Unlock) returns ErrAttemptInvalidated.
func (l *Limiter) RecordFailure(ctx context.Context, attempt Attempt) (Status, error) {
	if err := validateAttempt(attempt); err != nil {
		return Status{}, err
	}
	result, err := failureScript.Run(ctx, l.rdb, []string{l.stateKey(attempt.accountHash), l.attemptKey(attempt.accountHash, attempt.id)}, attempt.epoch, l.failureLimit, l.windowSec, l.lockSec, l.attemptTTL).Result()
	if err != nil {
		return Status{}, storeError(err)
	}
	items, ok := resultStrings(result)
	if !ok || len(items) == 0 {
		return Status{}, fmt.Errorf("%w: invalid failure result", ErrStoreUnavailable)
	}
	switch items[0] {
	case "INVALIDATED":
		return Status{}, ErrAttemptInvalidated
	case "LOCKED":
		status, ok := parseLockedStatus(items, 1)
		if !ok {
			return Status{}, fmt.Errorf("%w: invalid locked result", ErrStoreUnavailable)
		}
		return status, nil
	case "OK":
		if len(items) != 4 {
			return Status{}, fmt.Errorf("%w: invalid failure result", ErrStoreUnavailable)
		}
		failureCount, ok := parseInt(items[1])
		locked, lockedOK := parseInt(items[2])
		retry, retryOK := parseInt64(items[3])
		if !ok || !lockedOK || !retryOK {
			return Status{}, fmt.Errorf("%w: invalid failure result", ErrStoreUnavailable)
		}
		remaining := l.failureLimit - failureCount
		if remaining < 0 {
			remaining = 0
		}
		return Status{Locked: locked == 1, FailureCount: failureCount, RemainingAttempt: remaining, RetryAfter: secondsDuration(retry)}, nil
	default:
		return Status{}, fmt.Errorf("%w: unknown failure result %q", ErrStoreUnavailable, items[0])
	}
}

// RecordSuccess clears the account's failure state. If Unlock or another
// successful attempt already advanced the account epoch, the handle is stale.
func (l *Limiter) RecordSuccess(ctx context.Context, attempt Attempt) error {
	if err := validateAttempt(attempt); err != nil {
		return err
	}
	result, err := successScript.Run(ctx, l.rdb, []string{l.stateKey(attempt.accountHash), l.attemptKey(attempt.accountHash, attempt.id)}, attempt.epoch, l.attemptTTL).Result()
	if err != nil {
		return storeError(err)
	}
	items, ok := resultStrings(result)
	if !ok || len(items) == 0 {
		return fmt.Errorf("%w: invalid success result", ErrStoreUnavailable)
	}
	switch items[0] {
	case "OK":
		return nil
	case "INVALIDATED":
		return ErrAttemptInvalidated
	default:
		return fmt.Errorf("%w: unknown success result %q", ErrStoreUnavailable, items[0])
	}
}

// Unlock clears the failure state for an account and invalidates all attempts
// created before the unlock. It is idempotent when no state exists.
func (l *Limiter) Unlock(ctx context.Context, accountKey string) (UnlockResult, error) {
	hash, err := l.accountHash(accountKey)
	if err != nil {
		return UnlockResult{}, err
	}
	result, err := unlockScript.Run(ctx, l.rdb, []string{l.stateKey(hash)}, l.attemptTTL).Result()
	if err != nil {
		return UnlockResult{}, storeError(err)
	}
	items, ok := resultStrings(result)
	if !ok || len(items) != 1 {
		return UnlockResult{}, fmt.Errorf("%w: invalid unlock result", ErrStoreUnavailable)
	}
	cleared, ok := parseInt(items[0])
	if !ok || (cleared != 0 && cleared != 1) {
		return UnlockResult{}, fmt.Errorf("%w: invalid unlock result", ErrStoreUnavailable)
	}
	return UnlockResult{Cleared: cleared == 1}, nil
}

func (l *Limiter) accountHash(accountKey string) (string, error) {
	accountKey = strings.TrimSpace(accountKey)
	if accountKey == "" || len([]byte(accountKey)) > maxAccountKeyLen {
		return "", ErrAccountKeyRequired
	}
	for _, r := range accountKey {
		if r < 0x20 || r == 0x7F {
			return "", ErrAccountKeyRequired
		}
	}
	h := hmac.New(sha256.New, l.hmacKey)
	_, _ = h.Write([]byte(accountKey))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (l *Limiter) stateKey(accountHash string) string {
	return l.prefix + ":state:" + accountHash
}

func (l *Limiter) attemptKey(accountHash, id string) string {
	return l.prefix + ":attempt:" + accountHash + ":" + id
}

func validateAttempt(attempt Attempt) error {
	if attempt.id == "" || attempt.accountHash == "" || attempt.epoch <= 0 {
		return ErrAttemptRequired
	}
	return nil
}

func isNilUniversalClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrStoreUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

func durationSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func secondsDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func resultStrings(result any) ([]string, bool) {
	items, ok := result.([]interface{})
	if !ok {
		return nil, false
	}
	values := make([]string, len(items))
	for i, item := range items {
		switch value := item.(type) {
		case string:
			values[i] = value
		case []byte:
			values[i] = string(value)
		case int64:
			values[i] = fmt.Sprintf("%d", value)
		default:
			return nil, false
		}
	}
	return values, true
}

func parseInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

func parseInt64(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

func parseLockedStatus(items []string, offset int) (Status, bool) {
	if len(items) != offset+2 {
		return Status{}, false
	}
	failures, ok := parseInt(items[offset])
	if !ok {
		return Status{}, false
	}
	retry, ok := parseInt64(items[offset+1])
	if !ok {
		return Status{}, false
	}
	return Status{Locked: true, FailureCount: failures, RetryAfter: secondsDuration(retry)}, true
}

var beginScript = redis.NewScript(`
local now = tonumber(redis.call("TIME")[1])
local epoch = tonumber(redis.call("HGET", KEYS[1], "epoch")) or 0
local failures = tonumber(redis.call("HGET", KEYS[1], "failures")) or 0
local window_until = tonumber(redis.call("HGET", KEYS[1], "window_until")) or 0
local lock_until = tonumber(redis.call("HGET", KEYS[1], "lock_until")) or 0
local limit = tonumber(ARGV[2])
local window_seconds = tonumber(ARGV[3])
local attempt_ttl = tonumber(ARGV[5])

if lock_until > now then
  return {"LOCKED", tostring(failures), tostring(lock_until - now)}
end

if lock_until > 0 then
  epoch = epoch + 1
  failures = 0
  window_until = 0
  lock_until = 0
end
if window_until <= now then
  failures = 0
  window_until = now + window_seconds
end
if epoch <= 0 then epoch = 1 end

redis.call("HSET", KEYS[1], "epoch", epoch, "failures", failures, "window_until", window_until, "lock_until", lock_until)
local ttl = attempt_ttl
if window_until - now > ttl then ttl = window_until - now end
if lock_until - now > ttl then ttl = lock_until - now end
redis.call("EXPIRE", KEYS[1], ttl)
local created = redis.call("SET", KEYS[2], tostring(epoch), "EX", attempt_ttl, "NX")
if not created then return {"ATTEMPT_EXISTS"} end
local remaining = limit - failures
if remaining < 0 then remaining = 0 end
return {"OK", ARGV[1], tostring(epoch), tostring(failures), tostring(remaining)}
`)

var failureScript = redis.NewScript(`
local stored_epoch = redis.call("GET", KEYS[2])
if not stored_epoch then return {"INVALIDATED"} end
local current_epoch = redis.call("HGET", KEYS[1], "epoch")
if not current_epoch or current_epoch ~= ARGV[1] then
  redis.call("DEL", KEYS[2])
  return {"INVALIDATED"}
end
local now = tonumber(redis.call("TIME")[1])
local failures = tonumber(redis.call("HGET", KEYS[1], "failures")) or 0
local window_until = tonumber(redis.call("HGET", KEYS[1], "window_until")) or 0
local lock_until = tonumber(redis.call("HGET", KEYS[1], "lock_until")) or 0
if lock_until > now then
  redis.call("DEL", KEYS[2])
  return {"LOCKED", tostring(failures), tostring(lock_until - now)}
end
if lock_until > 0 or window_until <= now then
  redis.call("DEL", KEYS[2])
  return {"INVALIDATED"}
end

failures = failures + 1
if failures >= tonumber(ARGV[2]) then
  lock_until = now + tonumber(ARGV[4])
else
  lock_until = 0
end
redis.call("HSET", KEYS[1], "failures", failures, "lock_until", lock_until)
local ttl = tonumber(ARGV[5])
if window_until - now > ttl then ttl = window_until - now end
if lock_until - now > ttl then ttl = lock_until - now end
redis.call("EXPIRE", KEYS[1], ttl)
redis.call("DEL", KEYS[2])
local locked = 0
local retry = 0
if lock_until > now then locked = 1; retry = lock_until - now end
return {"OK", tostring(failures), tostring(locked), tostring(retry)}
`)

var successScript = redis.NewScript(`
local stored_epoch = redis.call("GET", KEYS[2])
if not stored_epoch then return {"INVALIDATED"} end
local current_epoch = redis.call("HGET", KEYS[1], "epoch")
if not current_epoch or current_epoch ~= ARGV[1] then
  redis.call("DEL", KEYS[2])
  return {"INVALIDATED"}
end
local next_epoch = tonumber(current_epoch) + 1
redis.call("HSET", KEYS[1], "epoch", next_epoch, "failures", 0, "window_until", 0, "lock_until", 0)
redis.call("EXPIRE", KEYS[1], tonumber(ARGV[2]))
redis.call("DEL", KEYS[2])
return {"OK"}
`)

var unlockScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then return {"0"} end
local failures = tonumber(redis.call("HGET", KEYS[1], "failures")) or 0
local lock_until = tonumber(redis.call("HGET", KEYS[1], "lock_until")) or 0
local epoch = tonumber(redis.call("HGET", KEYS[1], "epoch")) or 0
redis.call("HSET", KEYS[1], "epoch", epoch + 1, "failures", 0, "window_until", 0, "lock_until", 0)
redis.call("EXPIRE", KEYS[1], tonumber(ARGV[1]))
if failures > 0 or lock_until > 0 then return {"1"} end
return {"0"}
`)
