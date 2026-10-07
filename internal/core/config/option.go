package config

import "strings"

// Option 配置选项函数
type Option func(*Config)

// WithConfigFile 指定配置文件完整路径
func WithConfigFile(path string) Option {
	return func(c *Config) {
		c.configFile = path
	}
}

// WithConfigName 设置配置文件名（不含扩展名）
func WithConfigName(name string) Option {
	return func(c *Config) {
		c.configName = name
	}
}

// WithConfigType 设置配置文件类型（如 yaml, json, toml）
func WithConfigType(typ string) Option {
	return func(c *Config) {
		c.configType = typ
	}
}

// WithConfigPaths 设置配置文件搜索路径
func WithConfigPaths(paths ...string) Option {
	return func(c *Config) {
		c.configPaths = paths
	}
}

// WithProtected 设置是否启用保护模式
// 保护模式下，配置文件被外部修改后会自动恢复为原始内容
// 需要同时启用 WithAutoWatch 或在加载后调用 StartWatch，并保持管理器直到停机时关闭。
func WithProtected(protected bool) Option {
	return func(c *Config) {
		c.protected = protected
	}
}

// WithAutoWatch 设置是否自动开启文件监控
func WithAutoWatch(watch bool) Option {
	return func(c *Config) {
		c.autoWatch = watch
	}
}

// WithOnChange 设置配置变更回调函数
// 仅在非保护模式下，配置文件变更后触发
func WithOnChange(fn func()) Option {
	return func(c *Config) {
		c.onChange = fn
	}
}

// WithOnError 设置错误回调函数
// 当配置恢复失败等错误发生时触发
func WithOnError(fn func(error)) Option {
	return func(c *Config) {
		c.onError = fn
	}
}

// WithDefaults 设置默认配置值
func WithDefaults(defaults map[string]any) Option {
	return func(c *Config) {
		c.defaults = defaults
	}
}

// WithEnvPrefix 设置环境变量前缀
func WithEnvPrefix(prefix string) Option {
	return func(c *Config) {
		c.envPrefix = prefix
	}
}

// WithEnvKeyReplacer 设置环境变量键名替换器
func WithEnvKeyReplacer(r *strings.Replacer) Option {
	return func(c *Config) {
		c.envKeyReplacer = r
	}
}
