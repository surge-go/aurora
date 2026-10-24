package middleware

import (
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/surge-go/aurora"
)

// IPFilterController 提供运行时修改 IP 过滤规则的能力。
// 使用场景：动态封禁攻击 IP、从外部系统同步黑名单等。
type IPFilterController struct {
	mu        sync.RWMutex
	allowList []string
	blockList []string
	allowNets []netip.Prefix
	blockNets []netip.Prefix
}

// AddToAllowList 添加 IP 到白名单。
// 支持单个 IP 或 CIDR 表示法，例如 "192.168.1.100" 或 "10.0.0.0/8"。
func (ctrl *IPFilterController) AddToAllowList(ips ...string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if !contains(ctrl.allowList, ip) {
			ctrl.allowList = append(ctrl.allowList, ip)
		}
	}

	ctrl.allowNets = parseIPList(ctrl.allowList)
	return nil
}

// RemoveFromAllowList 从白名单移除 IP。
func (ctrl *IPFilterController) RemoveFromAllowList(ips ...string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	ctrl.allowList = removeIPs(ctrl.allowList, ips)
	ctrl.allowNets = parseIPList(ctrl.allowList)
	return nil
}

// SetAllowList 完全替换白名单。
func (ctrl *IPFilterController) SetAllowList(ips []string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	ctrl.allowList = append([]string(nil), ips...)
	ctrl.allowNets = parseIPList(ctrl.allowList)
	return nil
}

// GetAllowList 获取当前白名单（返回拷贝，线程安全）。
func (ctrl *IPFilterController) GetAllowList() []string {
	ctrl.mu.RLock()
	defer ctrl.mu.RUnlock()

	return append([]string(nil), ctrl.allowList...)
}

// AddToBlockList 添加 IP 到黑名单（封禁）。
// 支持单个 IP 或 CIDR 表示法，例如 "1.2.3.4" 或 "5.6.7.0/24"。
func (ctrl *IPFilterController) AddToBlockList(ips ...string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if !contains(ctrl.blockList, ip) {
			ctrl.blockList = append(ctrl.blockList, ip)
		}
	}

	ctrl.blockNets = parseIPList(ctrl.blockList)
	return nil
}

// RemoveFromBlockList 从黑名单移除 IP（解封）。
func (ctrl *IPFilterController) RemoveFromBlockList(ips ...string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	ctrl.blockList = removeIPs(ctrl.blockList, ips)
	ctrl.blockNets = parseIPList(ctrl.blockList)
	return nil
}

// SetBlockList 完全替换黑名单。
func (ctrl *IPFilterController) SetBlockList(ips []string) error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	ctrl.blockList = append([]string(nil), ips...)
	ctrl.blockNets = parseIPList(ctrl.blockList)
	return nil
}

// GetBlockList 获取当前黑名单（返回拷贝，线程安全）。
func (ctrl *IPFilterController) GetBlockList() []string {
	ctrl.mu.RLock()
	defer ctrl.mu.RUnlock()

	return append([]string(nil), ctrl.blockList...)
}

// IPFilterConfig 定义 IP 过滤中间件的配置。
type IPFilterConfig struct {
	// AllowList IP 白名单，支持单个 IP 或 CIDR 表示法。
	// 如果配置了白名单，只有在白名单中的 IP 才能访问。
	// 示例: []string{"192.168.1.100", "10.0.0.0/8"}
	AllowList []string

	// BlockList IP 黑名单，支持单个 IP 或 CIDR 表示法。
	// 黑名单中的 IP 会被拒绝访问。
	// 示例: []string{"203.0.113.0", "198.51.100.0/24"}
	BlockList []string

	// SkipPaths 跳过 IP 过滤检查的路径列表。
	// 支持精确匹配和前缀匹配（以 * 结尾）。
	// 示例: []string{"/health", "/public/*"}
	SkipPaths []string

	// TrustedProxies 信任的代理 IP 列表，用于从 X-Forwarded-For 提取真实 IP。
	// 如果为空，则直接使用 RemoteAddr。
	TrustedProxies []string

	// IPExtractor 自定义 IP 提取函数，如果为 nil 则使用默认提取逻辑。
	IPExtractor func(*aurora.Context) string

	// OnRejected 当请求被拒绝时的回调函数。
	// 如果为 nil，返回标准的 403 Forbidden 响应。
	OnRejected func(*aurora.Context, string)

	// Controller 运行时控制器，用于动态修改 IP 规则。
	// 如果提供，将从 Controller 读取规则而非静态配置。
	Controller *IPFilterController
}

// ipFilterRuntime 运行时配置，包含预解析的 IP 网络。
type ipFilterRuntime struct {
	allowNets      []netip.Prefix
	blockNets      []netip.Prefix
	skipPaths      []string
	trustedProxies []netip.Addr
	ipExtractor    func(*aurora.Context) string
	onRejected     func(*aurora.Context, string)
	controller     *IPFilterController // 动态控制器
}

// DefaultIPFilterConfig 返回空配置（无过滤）。
func DefaultIPFilterConfig() IPFilterConfig {
	return IPFilterConfig{}
}

// IPFilter 创建一个 IP 过滤中间件。
//
// 过滤逻辑：
// 1. 如果路径在 SkipPaths 中，跳过检查
// 2. 如果配置了白名单，检查 IP 是否在白名单中，不在则拒绝
// 3. 如果配置了黑名单，检查 IP 是否在黑名单中，在则拒绝
// 4. 否则允许访问
//
// 动态更新示例：
//
//	ctrl := &middleware.IPFilterController{}
//	engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
//	    AllowList: []string{"10.0.0.0/8"},
//	    Controller: ctrl,
//	}))
//
//	// 运行时封禁攻击 IP
//	ctrl.AddToBlockList("1.2.3.4")
func IPFilter(config IPFilterConfig) aurora.Middleware {
	runtime := &ipFilterRuntime{
		skipPaths:   config.SkipPaths,
		ipExtractor: config.IPExtractor,
		onRejected:  config.OnRejected,
		controller:  config.Controller,
	}

	// 如果有 controller，初始化其规则
	if runtime.controller != nil {
		runtime.controller.mu.Lock()
		runtime.controller.allowList = append([]string(nil), config.AllowList...)
		runtime.controller.blockList = append([]string(nil), config.BlockList...)
		runtime.controller.allowNets = parseIPList(config.AllowList)
		runtime.controller.blockNets = parseIPList(config.BlockList)
		runtime.controller.mu.Unlock()
	} else {
		// 没有 controller，使用静态配置
		if len(config.AllowList) > 0 {
			runtime.allowNets = parseIPList(config.AllowList)
		}
		if len(config.BlockList) > 0 {
			runtime.blockNets = parseIPList(config.BlockList)
		}
	}

	// 解析信任的代理
	if len(config.TrustedProxies) > 0 {
		runtime.trustedProxies = parseIPAddrs(config.TrustedProxies)
	}

	// 默认 IP 提取器
	if runtime.ipExtractor == nil {
		runtime.ipExtractor = func(c *aurora.Context) string {
			return extractClientIP(c, runtime.trustedProxies)
		}
	}

	// 默认拒绝处理
	if runtime.onRejected == nil {
		runtime.onRejected = func(c *aurora.Context, ip string) {
			c.Fail(aurora.ErrForbidden.WithMessage("access denied from IP: " + ip))
		}
	}

	return func(c *aurora.Context) {
		// 检查是否跳过此路径
		if shouldSkipAuth(c.Request().URL.Path, runtime.skipPaths) {
			c.Next()
			return
		}

		// 提取客户端 IP
		clientIP := runtime.ipExtractor(c)
		if clientIP == "" {
			runtime.onRejected(c, "unknown")
			return
		}

		// 解析 IP 地址
		addr, err := netip.ParseAddr(clientIP)
		if err != nil {
			runtime.onRejected(c, clientIP)
			return
		}

		// 获取当前规则（支持动态更新）
		var allowNets, blockNets []netip.Prefix
		if runtime.controller != nil {
			runtime.controller.mu.RLock()
			allowNets = runtime.controller.allowNets
			blockNets = runtime.controller.blockNets
			runtime.controller.mu.RUnlock()
		} else {
			allowNets = runtime.allowNets
			blockNets = runtime.blockNets
		}

		// 白名单检查：如果配置了白名单，IP 必须在白名单中
		if len(allowNets) > 0 {
			if !matchesAnyPrefix(addr, allowNets) {
				runtime.onRejected(c, clientIP)
				return
			}
		}

		// 黑名单检查：如果 IP 在黑名单中，拒绝访问
		if len(blockNets) > 0 {
			if matchesAnyPrefix(addr, blockNets) {
				runtime.onRejected(c, clientIP)
				return
			}
		}

		c.Next()
	}
}

// extractClientIP 从请求中提取客户端真实 IP。
func extractClientIP(c *aurora.Context, trustedProxies []netip.Addr) string {
	// 获取直连 IP
	remoteIP := getRemoteIP(c.Request().RemoteAddr)
	if remoteIP == "" {
		return ""
	}

	// 如果没有配置信任的代理，直接返回 RemoteAddr
	if len(trustedProxies) == 0 {
		return remoteIP
	}

	// 检查直连 IP 是否是信任的代理
	remoteAddr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return remoteIP
	}

	isTrusted := slices.Contains(trustedProxies, remoteAddr)

	if !isTrusted {
		return remoteIP
	}

	// 从 X-Forwarded-For 提取真实 IP
	if xff := strings.TrimSpace(c.GetHeader("X-Forwarded-For")); xff != "" {
		// X-Forwarded-For 格式: client, proxy1, proxy2
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			if clientIP := strings.TrimSpace(parts[0]); clientIP != "" {
				return clientIP
			}
		}
	}

	// 尝试 X-Real-IP
	if xri := strings.TrimSpace(c.GetHeader("X-Real-IP")); xri != "" {
		return xri
	}

	return remoteIP
}

// getRemoteIP 从 RemoteAddr 中提取 IP（去掉端口号）。
func getRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// 可能没有端口号
		return remoteAddr
	}
	return host
}

// parseIPList 解析 IP 列表，支持单个 IP 和 CIDR。
func parseIPList(list []string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		// 尝试解析为 CIDR
		if strings.Contains(item, "/") {
			if prefix, err := netip.ParsePrefix(item); err == nil {
				prefixes = append(prefixes, prefix)
				continue
			}
		}

		// 尝试解析为单个 IP
		if addr, err := netip.ParseAddr(item); err == nil {
			// 单个 IP 转换为 /32 (IPv4) 或 /128 (IPv6) 前缀
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			prefix := netip.PrefixFrom(addr, bits)
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// parseIPAddrs 解析 IP 地址列表。
func parseIPAddrs(list []string) []netip.Addr {
	var addrs []netip.Addr
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// matchesAnyPrefix 检查 IP 是否匹配任何前缀。
func matchesAnyPrefix(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// contains 检查字符串是否在切片中。
func contains(list []string, item string) bool {
	return slices.Contains(list, item)
}

// removeIPs 从列表中移除指定的 IP。
func removeIPs(list []string, toRemove []string) []string {
	removeSet := make(map[string]struct{}, len(toRemove))
	for _, ip := range toRemove {
		removeSet[strings.TrimSpace(ip)] = struct{}{}
	}

	result := make([]string, 0, len(list))
	for _, ip := range list {
		if _, found := removeSet[ip]; !found {
			result = append(result, ip)
		}
	}
	return result
}
