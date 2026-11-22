// MCP 调用限流：令牌桶（token bucket），普通工具与重量工具分开计数。
//
// 配置写在 config/settings.yaml 的 mcp 段，运行中会按文件修改时间热加载，
// 改完无需重启 exe：
//
//	mcp:
//	  enabled: true
//	  bucket_capacity: 8          # 普通工具桶容量
//	  refill_per_second: 0.25     # 普通工具桶补充速率（个/秒）
//	  heavy_tools: [sync_all, sync_account, create_pr]
//	  heavy_capacity: 2
//	  heavy_refill_per_second: 0.1
package mcp

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github-multi-account/internal/config"
)

// tokenBucket 是线程安全的令牌桶。
type tokenBucket struct {
	mu           sync.Mutex
	tokens       float64
	last         time.Time
	capacity     float64
	refillPerSec float64
}

func newTokenBucket(capacity, refillPerSec float64) *tokenBucket {
	if capacity < 1 {
		capacity = 1
	}
	if refillPerSec <= 0 {
		refillPerSec = 0.01
	}
	return &tokenBucket{tokens: capacity, last: time.Now(), capacity: capacity, refillPerSec: refillPerSec}
}

// take 尝试取出一个令牌，返回是否成功以及需要等待的秒数。
func (b *tokenBucket) take() (bool, float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = math.Min(b.capacity, b.tokens+now.Sub(b.last).Seconds()*b.refillPerSec)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, (1 - b.tokens) / b.refillPerSec
}

// describe 返回桶的可读描述（用于错误信息与状态展示）。
func (b *tokenBucket) describe() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	secondsPerToken := 0.0
	if b.refillPerSec > 0 {
		secondsPerToken = 1 / b.refillPerSec
	}
	return fmt.Sprintf("容量 %.0f，每 %.0f 秒补充 1 个令牌，当前剩余 %.1f",
		b.capacity, secondsPerToken, b.tokens)
}

// setCapacity 热更新容量：扩容时补满令牌（用户主动放宽），
// 缩容时保留已消耗状态（不会把限流"重置"放宽）。
func (b *tokenBucket) setCapacity(v float64) {
	if v < 1 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if v > b.capacity {
		b.tokens = v
	} else if b.tokens > v {
		b.tokens = v
	}
	b.capacity = v
}

// setRefill 热更新补充速率（个/秒）。
func (b *tokenBucket) setRefill(v float64) {
	if v <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillPerSec = v
}

// limiter 保存限流配置与两个桶，并按 settings.yaml 的修改时间热加载。
type limiter struct {
	dir string

	mu        sync.Mutex
	normal    *tokenBucket
	heavy     *tokenBucket
	heavySet  map[string]bool
	enabled   bool
	modTime   time.Time
	loaded    bool
	configErr error
}

// newLimiter 用默认配置创建限流器并立即加载一次文件配置。
func newLimiter(dir string) *limiter {
	l := &limiter{dir: dir, enabled: true}
	l.normal = newTokenBucket(8, 0.25)
	l.heavy = newTokenBucket(2, 0.1)
	l.heavySet = map[string]bool{"sync_all": true, "sync_account": true, "create_pr": true}
	l.reload()
	return l
}

// reload 检查 settings.yaml 是否变化，变化则应用 mcp 段配置。
func (l *limiter) reload() {
	path := l.dir + string(os.PathSeparator) + "settings.yaml"
	info, err := os.Stat(path)
	if err != nil {
		return // 没有配置文件时沿用默认值
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded && !info.ModTime().After(l.modTime) {
		return
	}
	l.modTime = info.ModTime()
	l.loaded = true

	raw, ok := config.ReadSettings(l.dir)["mcp"].(map[string]any)
	if !ok {
		return
	}
	if v, ok := raw["enabled"].(bool); ok {
		l.enabled = v
	}
	if v, ok := asFloat(raw["bucket_capacity"]); ok && v > 0 {
		l.normal.setCapacity(v)
	}
	if v, ok := asFloat(raw["refill_per_second"]); ok && v > 0 {
		l.normal.setRefill(v)
	}
	// 注意：键存在但为空数组时，表示清空重量工具集合
	if rawVal, exists := raw["heavy_tools"]; exists {
		set := map[string]bool{}
		for _, t := range toStringSlice(rawVal) {
			if t = strings.TrimSpace(t); t != "" {
				set[t] = true
			}
		}
		l.heavySet = set
	}
	if v, ok := asFloat(raw["heavy_capacity"]); ok && v > 0 {
		l.heavy.setCapacity(v)
	}
	if v, ok := asFloat(raw["heavy_refill_per_second"]); ok && v > 0 {
		l.heavy.setRefill(v)
	}
}

// allow 判断某工具是否允许调用，返回 (是否放行, 建议等待秒数, 提示信息)。
func (l *limiter) allow(tool string) (bool, float64, string) {
	l.reload()
	l.mu.Lock()
	enabled := l.enabled
	heavy := l.heavySet[tool]
	l.mu.Unlock()
	if !enabled {
		return true, 0, ""
	}
	if heavy {
		ok, wait := l.heavy.take()
		if ok {
			return true, 0, ""
		}
		return false, wait, "重量工具桶：" + l.heavy.describe()
	}
	ok, wait := l.normal.take()
	if ok {
		return true, 0, ""
	}
	return false, wait, "普通工具桶：" + l.normal.describe()
}

// status 返回当前限流配置与桶状态（供 mcp_status 工具展示）。
func (l *limiter) status() map[string]any {
	l.reload()
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{
		"enabled":   l.enabled,
		"heavy":     l.heavySet,
		"normal":    l.normal.describe(),
		"heavy_desc": l.heavy.describe(),
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// toStringSlice 兼容 YAML 数组与逗号分隔字符串两种写法。
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		parts := strings.Split(t, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out
	default:
		return nil
	}
}
