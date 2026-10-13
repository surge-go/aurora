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

// issueScript 原子化完成"签发新会话"的全部 Redis 写入：
//
//  1. 幂等检查：命中幂等键且指纹一致时直接返回缓存的加密 token pair；
//     指纹不一致返回 IDEMPOTENCY_CONFLICT。
//  2. 扫描主体索引 ZSET，清理已不存在的会话成员，统计：
//     当前活跃数、当前平台数、被标记为互斥的平台数。
//  3. 根据全局/平台/互斥三类上限计算需要踢出的会话数量。
//  4. strategy=latest 且未指定 force_login 时直接返回 CONFLICT。
//  5. 否则按策略选出待踢出的会话，删除会话 hash、refresh 键和索引成员。
//  6. 写入新会话 hash、refresh hash、各索引 ZSET，并设置幂等键。
//
// 由于整段脚本在 Redis 单线程中原子执行，并发登录不会突破任何一类上限。
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
local idempotency_fingerprint = ARGV[18 + exclusive_count]
local idempotency_payload = ARGV[19 + exclusive_count]
local idempotency_ttl = tonumber(ARGV[20 + exclusive_count]) or 0

if idempotency_fingerprint ~= "" then
  local existing = redis.call("GET", KEYS[5])
  if existing then
    local separator = string.find(existing, "|", 1, true)
    if not separator or string.sub(existing, 1, separator - 1) ~= idempotency_fingerprint then
      return {"IDEMPOTENCY_CONFLICT"}
    end
    return {"IDEMPOTENCY_REPLAY", string.sub(existing, separator + 1)}
  end
end

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
if idempotency_fingerprint ~= "" and idempotency_ttl > 0 then
  redis.call("SET", KEYS[5], idempotency_fingerprint .. "|" .. idempotency_payload, "EX", idempotency_ttl)
end
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

// refreshScript 原子化完成 refresh token 轮换：
//
//  1. 会话不存在 → NOT_FOUND；
//  2. 当前 refresh hash 与期望不一致 → REUSED（防止同一 token 重放）；
//  3. 会话已过期 → EXPIRED；
//  4. 否则写入新的 refresh hash、过期时间，并刷新主体/平台/设备索引。
//
// 整个过程是单脚本原子操作，即使并发使用同一 refresh token 也只有
// 一个调用方会成功，其余全部命中 REUSED。
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

// revokeSessionScript 原子化删除单个会话及其在 subject / platform / device
// 索引中的成员，空索引会被一并清理，避免空 ZSET 长期占用内存。
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

// revokeSubjectPageScript 按分页方式撤销主体下的会话：
//
// 每次最多处理 limit 条会话，逐条删除会话 hash、refresh 键和 platform /
// device 索引，最后返回 [已撤销数, 是否还有剩余]。
// 分页设计避免主体拥有大量会话时单次 Lua 脚本阻塞 Redis。
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

// issueScriptInput 是 issueScript 的入参集合。字段顺序必须与 Lua 中 ARGV 顺序一一对应。
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
	IdempotencyFingerprint, IdempotencyPayload        string
	IdempotencyTTL                                    int64
}

// issueScriptArgs 将 issueScriptInput 序列化为 Lua 脚本期望的 ARGV 列表，
// ExclusiveWith 为变长字段，紧跟在固定字段之后，因此 prefix 与幂等参数的位置需动态计算。
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
	args = append(args, input.IdempotencyFingerprint, input.IdempotencyPayload, strconv.FormatInt(input.IdempotencyTTL, 10))
	return args
}

// refreshScriptInput 是 refreshScript 的入参集合。
type refreshScriptInput struct {
	ExpectedHash, NewHash                  string
	Now, ExpiresAt, LastRefreshed, TTL     int64
	Prefix, SubjectKey, Platform, DeviceID string
	SessionID                              string
}

// refreshScriptArgs 将 refreshScriptInput 序列化为 Lua 脚本期望的 ARGV 列表。
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

// boolString 将 bool 编码为 Lua 端可比较的 "1"/"0"，与 ARGV 中的字符串语义保持一致。
func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// loadSession 加载会话完整状态（元数据 + 当前 refresh hash）。
// refresh hash 单独存放在 :session_refresh:{id} key，便于轮换时原子更新。
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

// loadSessionMetadata 仅加载会话元数据（不取 refresh hash），用于 Revoke 等
// 不需要比对 hash 的场景，减少一次往返。
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

// parseSessionTime 解析 Redis 中存储的 Unix 秒时间戳；非正数视为会话数据损坏。
func parseSessionTime(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, fmt.Errorf("%w: invalid session timestamp", ErrSessionMismatch)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// scriptStatus 从 Lua 脚本的多种返回形式（string / []byte / []interface{} / int64 / int）
// 提取首个状态字符串，便于上层使用 switch 统一分支处理。
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

// scriptValues 把 Lua 返回的列表统一转为 []int64。
// go-redis 在不同协议版本下可能返回 int64、string 或 []byte，这里做兼容处理。
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

// storeError 把 Redis 调用错误统一包装为 ErrStoreUnavailable，
// 让上层能够通过 errors.Is 稳定识别"基础设施不可用"这一类失败。
func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrStoreUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

// 以下 sessionKey / refreshKey / subjectIndexKey / platformIndexKey / deviceIndexKey
// 是本包使用的全部 Redis key 布局。所有 key 共享同一前缀，
// 前缀中的 {} hash tag 保证 Cluster 模式下它们落到同一 slot，Lua 脚本可以原子访问。
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
