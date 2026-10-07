package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// snapshot 保存配置文件快照，用于保护模式下的恢复
type snapshot struct {
	content []byte
	mode    os.FileMode
}

// startWatch 开始监控配置文件变更
// 调用方必须持有 c.mu 锁。
func (c *Config) startWatch() error {
	if c.watching {
		return nil
	}

	file := c.viper.ConfigFileUsed()
	if file == "" {
		return fmt.Errorf("configuration must be loaded before watching")
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("resolve configuration path: %w", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(file)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return err
	}

	c.watcher = w
	c.watching = true
	go c.watchLoop(w, filepath.Clean(file))
	return nil
}

func (c *Config) watchLoop(w *fsnotify.Watcher, file string) {
	defer func() {
		c.mu.Lock()
		if c.watcher == w {
			c.watcher = nil
			c.watching = false
		}
		c.mu.Unlock()
	}()

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != file ||
				!event.Has(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) {
				continue
			}
			c.handleConfigChange(w, file)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			c.reportError(fmt.Errorf("配置监听失败: %w", err))
		}
	}
}

func (c *Config) handleConfigChange(w *fsnotify.Watcher, file string) {
	if c.restoring.Load() {
		c.pending.Store(true)
		return
	}

	c.mu.Lock()
	if !c.watching || c.watcher != w {
		c.mu.Unlock()
		return
	}
	protected := c.protected
	onChange := c.onChange
	snapContent := c.copySnapContent()
	if protected {
		current, err := os.ReadFile(file)
		if err == nil && bytes.Equal(current, snapContent) {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		c.restoreFromContent(w, file, snapContent)
		return
	}

	// Viper 不支持并发读写，先在锁内重新加载，再向读取方和回调暴露新值。
	err := c.viper.ReadInConfig()
	c.mu.Unlock()
	if err != nil {
		c.reportError(fmt.Errorf("重新加载配置失败: %w", err))
		return
	}
	if onChange != nil {
		onChange()
	}
}

// StopWatch 停止监控配置文件并释放底层 watcher。
func (c *Config) StopWatch() {
	c.mu.Lock()
	c.watching = false
	w := c.watcher
	c.watcher = nil
	c.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}

// StartWatch 开始监控配置文件变更
// 如果已经在监控中，则不重复启动
func (c *Config) StartWatch() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.watching {
		return nil
	}

	return c.startWatch()
}

// SetProtected 设置保护模式
func (c *Config) SetProtected(protected bool) {
	c.mu.Lock()
	var snapErr error
	if protected {
		snapErr = c.saveSnapshot()
		if snapErr == nil {
			c.protected = true
		}
	} else {
		c.protected = false
		c.snap = nil
	}
	c.mu.Unlock()

	// 释放锁后报告快照错误，避免在锁内调用用户回调
	if snapErr != nil {
		c.reportError(snapErr)
	}
}

// IsProtected 查询是否处于保护模式
func (c *Config) IsProtected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.protected
}

// saveSnapshot 保存当前配置文件快照
// 注意：调用方必须已持有 mu 锁，此方法内不再加锁
// 返回错误供调用方在释放锁后报告，避免在锁内调用用户回调导致死锁
func (c *Config) saveSnapshot() error {
	file := c.viper.ConfigFileUsed()
	if file == "" {
		return nil
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("保存快照失败: %w", err)
	}

	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("保存快照失败: %w", err)
	}
	c.snap = &snapshot{content: data, mode: info.Mode().Perm()}
	return nil
}

// copySnapContent 在锁保护下复制快照内容
// 调用方必须持有 mu.RLock
func (c *Config) copySnapContent() []byte {
	if c.snap == nil {
		return nil
	}
	cp := make([]byte, len(c.snap.content))
	copy(cp, c.snap.content)
	return cp
}

// restoreFromContent 使用给定内容恢复配置文件
// 使用临时文件 + 原子替换确保可靠性
func (c *Config) restoreFromContent(w *fsnotify.Watcher, file string, content []byte) {
	if content == nil {
		return
	}

	// 标记正在恢复，防止恢复写入触发二次事件
	c.restoring.Store(true)
	defer func() {
		c.restoring.Store(false)
		if c.pending.Swap(false) {
			go c.handleConfigChange(w, file)
		}
	}()

	dir := filepath.Dir(file)
	tmp, err := os.CreateTemp(dir, ".config-restore-*")
	if err != nil {
		c.reportError(fmt.Errorf("创建临时文件失败: %w", err))
		return
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(fmt.Errorf("写入临时文件失败: %w", err))
		return
	}
	c.mu.RLock()
	mode := os.FileMode(0600)
	if c.snap != nil {
		mode = c.snap.mode
	}
	c.mu.RUnlock()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(fmt.Errorf("设置临时文件权限失败: %w", err))
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(fmt.Errorf("同步临时文件失败: %w", err))
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		c.reportError(fmt.Errorf("关闭临时文件失败: %w", err))
		return
	}

	if err := os.Rename(tmpName, file); err != nil {
		os.Remove(tmpName)
		c.reportError(fmt.Errorf("恢复配置文件失败: %w", err))
		return
	}

	// 恢复文件后重新读取，确保 viper 内存状态与文件一致。
	c.mu.Lock()
	err = c.viper.ReadInConfig()
	c.mu.Unlock()
	if err != nil {
		c.reportError(fmt.Errorf("恢复后重新加载配置失败: %w", err))
	}
}

// reportError 报告错误，优先使用 onError 回调，否则输出到 stderr
func (c *Config) reportError(err error) {
	c.mu.RLock()
	onError := c.onError
	c.mu.RUnlock()

	if onError != nil {
		onError(err)
	} else {
		fmt.Fprintf(os.Stderr, "[config] %v\n", err)
	}
}
