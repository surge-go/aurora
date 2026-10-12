package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

var issueScript = redis.NewScript(`
local session_id = ARGV[1]
local refresh_hash = ARGV[2]
local principal_json = ARGV[3]
local subject_key = ARGV[4]
local auth_version = ARGV[5]
local platform = ARGV[6]
local device_id = ARGV[7]
local issued_at = ARGV[8]
local expires_at = ARGV[9]
local absolute_expires_at = ARGV[10]
local ttl = ARGV[11]
local max_sessions = tonumber(ARGV[12]) or 0
local platform_max = tonumber(ARGV[13]) or 0
local strategy = ARGV[14]
local force_login = ARGV[15] == "1"
local exclusive_count = tonumber(ARGV[16]) or 0
local exclusive = {}
for i = 1, exclusive_count do
  exclusive[ARGV[16 + i]] = true
end
local prefix = ARGV[17 + exclusive_count]

local sessions = {}
local candidates = {}
local active_count = 0
local platform_count = 0
local exclusive_count_found = 0

if max_sessions > 0 or platform_max > 0 or exclusive_count > 0 then
  local entries = redis.call("ZRANGE", KEYS[3], 0, -1, "WITHSCORES")
  for i = 1, #entries, 2 do
    local old_id = entries[i]
    local score = entries[i + 1]
    local old_session_key = prefix .. ":session:" .. old_id
    if redis.call("EXISTS", old_session_key) == 0 then
      redis.call("ZREM", KEYS[3], old_id)
    else
      local old_platform = redis.call("HGET", old_session_key, "platform") or ""
      local old_device = redis.call("HGET", old_session_key, "device_id") or ""
      local item = { id = old_id, score = score, platform = old_platform, device = old_device }
      table.insert(sessions, item)
      active_count = active_count + 1
      if old_platform == platform then
        platform_count = platform_count + 1
      end
      if exclusive[old_platform] then
        exclusive_count_found = exclusive_count_found + 1
      end
    end
  end
end

local global_required = 0
if max_sessions > 0 and active_count >= max_sessions then
  global_required = active_count - max_sessions + 1
end
local platform_required = 0
if platform_max > 0 and platform_count >= platform_max then
  platform_required = platform_count - platform_max + 1
end
local has_conflict = global_required > 0 or platform_required > 0 or exclusive_count_found > 0

for _, item in ipairs(sessions) do
  if global_required > 0 or (platform_required > 0 and item.platform == platform) or exclusive[item.platform] then
    table.insert(candidates, item)
  end
end

if has_conflict and strategy == "latest" and not force_login then
  return {"CONFLICT"}
end

local function remove_session(item)
  local old_session_key = prefix .. ":session:" .. item.id
  local old_refresh_key = prefix .. ":session_refresh:" .. item.id
  local old_platform_key = prefix .. ":platform_sessions:" .. subject_key .. ":" .. item.platform
  redis.call("DEL", old_session_key, old_refresh_key)
  redis.call("ZREM", KEYS[3], item.id)
  redis.call("ZREM", old_platform_key, item.id)
  if redis.call("ZCARD", old_platform_key) == 0 then redis.call("DEL", old_platform_key) end
  if item.device ~= "" then
    local old_device_key = prefix .. ":device_sessions:" .. subject_key .. ":" .. item.device
    redis.call("ZREM", old_device_key, item.id)
    if redis.call("ZCARD", old_device_key) == 0 then redis.call("DEL", old_device_key) end
  end
end

local selected = {}
local function mark(item)
  selected[item.id] = true
end

local function mark_until(target, predicate)
  local matched = 0
  local start = 1
  local finish = #sessions
  local step = 1
  if strategy == "latest" then
    start = #sessions
    finish = 1
    step = -1
  end
  local i = start
  while true do
    if predicate(sessions[i]) and not selected[sessions[i].id] then
      mark(sessions[i])
      matched = matched + 1
      if matched >= target then
        return
      end
    end
    if i == finish then
      return
    end
    i = i + step
  end
end

if has_conflict then
  if strategy == "all" then
    for _, item in ipairs(candidates) do
      mark(item)
    end
  end
  if strategy ~= "all" then
    if exclusive_count_found > 0 then
      for _, item in ipairs(sessions) do
        if exclusive[item.platform] then
          mark(item)
        end
      end
    end
    if global_required > 0 then
      mark_until(global_required, function() return true end)
    end
    if platform_required > 0 then
      mark_until(platform_required, function(item) return item.platform == platform end)
    end
  end
  for _, item in ipairs(sessions) do
    if selected[item.id] then
      remove_session(item)
    end
  end
end

redis.call("HSET", KEYS[1],
  "principal_json", principal_json,
  "subject_key", subject_key,
  "auth_version", auth_version,
  "issued_at", issued_at,
  "last_refreshed_at", issued_at,
  "expires_at", expires_at,
  "absolute_expires_at", absolute_expires_at,
  "platform", platform,
  "device_id", device_id)
redis.call("EXPIRE", KEYS[1], ttl)
redis.call("SET", KEYS[2], refresh_hash, "EX", ttl)
redis.call("ZADD", KEYS[3], issued_at, session_id)
redis.call("ZADD", KEYS[4], issued_at, session_id)
if device_id ~= "" then
  redis.call("ZADD", prefix .. ":device_sessions:" .. subject_key .. ":" .. device_id, issued_at, session_id)
end
redis.call("EXPIRE", KEYS[3], ttl)
redis.call("EXPIRE", KEYS[4], ttl)
if device_id ~= "" then
  redis.call("EXPIRE", prefix .. ":device_sessions:" .. subject_key .. ":" .. device_id, ttl)
end
return {"OK"}
`)

var refreshScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  return {"NOT_FOUND"}
end
local current_hash = redis.call("GET", KEYS[2])
if not current_hash or current_hash ~= ARGV[1] then
  return {"REUSED"}
end
local current_expires = tonumber(redis.call("HGET", KEYS[1], "expires_at") or "0")
if current_expires <= tonumber(ARGV[3]) then
  return {"EXPIRED"}
end
redis.call("HSET", KEYS[1], "last_refreshed_at", ARGV[6], "expires_at", ARGV[4])
redis.call("EXPIRE", KEYS[1], ARGV[5])
redis.call("SET", KEYS[2], ARGV[2], "EX", ARGV[5])
local subject_index = ARGV[7] .. ":subject_sessions:" .. ARGV[8]
local platform_index = ARGV[7] .. ":platform_sessions:" .. ARGV[8] .. ":" .. ARGV[9]
local issued_at = redis.call("HGET", KEYS[1], "issued_at") or ARGV[6]
redis.call("ZADD", subject_index, issued_at, ARGV[11])
redis.call("ZADD", platform_index, issued_at, ARGV[11])
if ARGV[10] ~= "" then
  redis.call("ZADD", ARGV[7] .. ":device_sessions:" .. ARGV[8] .. ":" .. ARGV[10], issued_at, ARGV[11])
end
redis.call("EXPIRE", subject_index, ARGV[5])
redis.call("EXPIRE", platform_index, ARGV[5])
if ARGV[10] ~= "" then
  redis.call("EXPIRE", ARGV[7] .. ":device_sessions:" .. ARGV[8] .. ":" .. ARGV[10], ARGV[5])
end
return {"OK"}
`)

var revokeSessionScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 0
end
redis.call("DEL", KEYS[1], KEYS[2])
redis.call("ZREM", KEYS[3], ARGV[2])
redis.call("ZREM", KEYS[4], ARGV[3])
if ARGV[4] ~= "" then
  redis.call("ZREM", KEYS[5], ARGV[4])
end
if redis.call("ZCARD", KEYS[3]) == 0 then redis.call("DEL", KEYS[3]) end
if redis.call("ZCARD", KEYS[4]) == 0 then redis.call("DEL", KEYS[4]) end
if ARGV[4] ~= "" and redis.call("ZCARD", KEYS[5]) == 0 then redis.call("DEL", KEYS[5]) end
return 1
`)

var revokeSubjectPageScript = redis.NewScript(`
local prefix = ARGV[1]
local subject_key = ARGV[2]
local limit = tonumber(ARGV[3])
local entries = redis.call("ZRANGE", KEYS[1], 0, limit - 1)
local revoked = 0
for _, session_id in ipairs(entries) do
  local session_key = prefix .. ":session:" .. session_id
  if redis.call("EXISTS", session_key) == 1 then
    local platform = redis.call("HGET", session_key, "platform") or ""
    local device_id = redis.call("HGET", session_key, "device_id") or ""
    local platform_key = prefix .. ":platform_sessions:" .. subject_key .. ":" .. platform
    redis.call("DEL", session_key, prefix .. ":session_refresh:" .. session_id)
    redis.call("ZREM", platform_key, session_id)
    if redis.call("ZCARD", platform_key) == 0 then redis.call("DEL", platform_key) end
    if device_id ~= "" then
      local device_key = prefix .. ":device_sessions:" .. subject_key .. ":" .. device_id
      redis.call("ZREM", device_key, session_id)
      if redis.call("ZCARD", device_key) == 0 then redis.call("DEL", device_key) end
    end
    revoked = revoked + 1
  end
  redis.call("ZREM", KEYS[1], session_id)
end
if redis.call("ZCARD", KEYS[1]) > 0 then
  return {revoked, 1}
end
redis.call("DEL", KEYS[1])
return {revoked, 0}
`)

type issueScriptInput struct {
	SessionID, RefreshHash, PrincipalJSON, SubjectKey string
	AuthVersion                                       int64
	Platform, DeviceID                                string
	IssuedAt, ExpiresAt, AbsoluteExpiresAt, TTL       int64
	MaxSessions, PlatformMax                          int
	Strategy                                          KickoutStrategy
	ForceLogin                                        bool
	ExclusiveWith                                     []string
	Prefix                                            string
}

func issueScriptArgs(input issueScriptInput) []interface{} {
	args := []interface{}{
		input.SessionID,
		input.RefreshHash,
		input.PrincipalJSON,
		input.SubjectKey,
		strconv.FormatInt(input.AuthVersion, 10),
		input.Platform,
		input.DeviceID,
		strconv.FormatInt(input.IssuedAt, 10),
		strconv.FormatInt(input.ExpiresAt, 10),
		strconv.FormatInt(input.AbsoluteExpiresAt, 10),
		strconv.FormatInt(input.TTL, 10),
		strconv.Itoa(input.MaxSessions),
		strconv.Itoa(input.PlatformMax),
		string(input.Strategy),
		boolString(input.ForceLogin),
		strconv.Itoa(len(input.ExclusiveWith)),
	}
	for _, platform := range input.ExclusiveWith {
		args = append(args, platform)
	}
	args = append(args, input.Prefix)
	return args
}

type refreshScriptInput struct {
	ExpectedHash, NewHash                  string
	Now, ExpiresAt, LastRefreshed, TTL     int64
	Prefix, SubjectKey, Platform, DeviceID string
	SessionID                              string
}

func refreshScriptArgs(input refreshScriptInput) []interface{} {
	return []interface{}{
		input.ExpectedHash,
		input.NewHash,
		strconv.FormatInt(input.Now, 10),
		strconv.FormatInt(input.ExpiresAt, 10),
		strconv.FormatInt(input.TTL, 10),
		strconv.FormatInt(input.LastRefreshed, 10),
		input.Prefix,
		input.SubjectKey,
		input.Platform,
		input.DeviceID,
		input.SessionID,
	}
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (m *Manager) loadSession(ctx context.Context, sessionID string) (Session, error) {
	session, err := m.loadSessionMetadata(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	refreshHash, err := m.rdb.Get(ctx, m.refreshKey(sessionID)).Result()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, storeError(err)
	}
	session.CurrentRefreshHash = refreshHash
	return session, nil
}

func (m *Manager) loadSessionMetadata(ctx context.Context, sessionID string) (Session, error) {
	values, err := m.rdb.HGetAll(ctx, m.sessionKey(sessionID)).Result()
	if err != nil {
		return Session{}, storeError(err)
	}
	if len(values) == 0 {
		return Session{}, ErrSessionNotFound
	}
	var principal Principal
	if err := json.Unmarshal([]byte(values["principal_json"]), &principal); err != nil {
		return Session{}, fmt.Errorf("%w: invalid session principal", ErrSessionMismatch)
	}
	issuedAt, err := parseSessionTime(values["issued_at"])
	if err != nil {
		return Session{}, err
	}
	lastRefreshedAt, err := parseSessionTime(values["last_refreshed_at"])
	if err != nil {
		return Session{}, err
	}
	expiresAt, err := parseSessionTime(values["expires_at"])
	if err != nil {
		return Session{}, err
	}
	absoluteExpiresAt, err := parseSessionTime(values["absolute_expires_at"])
	if err != nil {
		return Session{}, err
	}
	authVersion, err := strconv.ParseInt(values["auth_version"], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("%w: invalid auth version", ErrSessionMismatch)
	}
	return Session{
		ID:                sessionID,
		Principal:         principal,
		AuthVersion:       authVersion,
		IssuedAt:          issuedAt,
		LastRefreshedAt:   lastRefreshedAt,
		ExpiresAt:         expiresAt,
		AbsoluteExpiresAt: absoluteExpiresAt,
		Platform:          values["platform"],
		DeviceID:          values["device_id"],
	}, nil
}

func parseSessionTime(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, fmt.Errorf("%w: invalid session timestamp", ErrSessionMismatch)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func scriptStatus(result interface{}) string {
	switch value := result.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	case []interface{}:
		if len(value) == 0 {
			return ""
		}
		return scriptStatus(value[0])
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	default:
		return ""
	}
}

func scriptValues(result interface{}) ([]int64, bool) {
	items, ok := result.([]interface{})
	if !ok {
		return nil, false
	}
	values := make([]int64, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case int64:
			values = append(values, value)
		case string:
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, false
			}
			values = append(values, parsed)
		case []byte:
			parsed, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil {
				return nil, false
			}
			values = append(values, parsed)
		default:
			return nil, false
		}
	}
	return values, true
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrStoreUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

func (m *Manager) sessionKey(sessionID string) string {
	return m.prefix + ":session:" + sessionID
}

func (m *Manager) refreshKey(sessionID string) string {
	return m.prefix + ":session_refresh:" + sessionID
}

func (m *Manager) subjectIndexKey(subjectKey string) string {
	return m.prefix + ":subject_sessions:" + subjectKey
}

func (m *Manager) platformIndexKey(subjectKey, platform string) string {
	return m.prefix + ":platform_sessions:" + subjectKey + ":" + platform
}

func (m *Manager) deviceIndexKey(subjectKey, deviceID string) string {
	return m.prefix + ":device_sessions:" + subjectKey + ":" + deviceID
}
