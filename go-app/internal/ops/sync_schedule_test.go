package ops

import (
	"testing"
	"time"
)

func TestNextIntervalAdaptive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QuotaLowRatio = 0.5
	cfg.MaxIntervalSeconds = 3600

	// 跳过占比达到阈值 → 拉长到基础间隔的 2 倍
	if got := nextInterval(900, 900, 4, 2, 60*time.Second, cfg); got != 1800 {
		t.Fatalf("配额紧张时应拉长到 1800，得到 %d", got)
	}
	// 低于阈值 → 回到基础间隔
	if got := nextInterval(900, 900, 4, 1, 60*time.Second, cfg); got != 900 {
		t.Fatalf("跳过占比不足时应回到基础间隔，得到 %d", got)
	}
	// 关闭自适应
	cfg.AdaptiveInterval = false
	if got := nextInterval(900, 900, 4, 4, 60*time.Second, cfg); got != 900 {
		t.Fatalf("关闭自适应后应回到基础间隔，得到 %d", got)
	}
	// 拉长不得超过上限
	cfg.AdaptiveInterval = true
	if got := nextInterval(900, 3600, 10, 10, 60*time.Second, cfg); got != 3600 {
		t.Fatalf("不应超过 max_interval_seconds，得到 %d", got)
	}
}

func TestNextIntervalElapsedProtection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdaptiveInterval = false
	if got := nextInterval(900, 900, 5, 0, 1000*time.Second, cfg); got <= 900 {
		t.Fatalf("本轮耗时应触发延长，得到 %d", got)
	}
}

func TestEngineCooldown(t *testing.T) {
	e := &Engine{lastSync: map[int]time.Time{}}
	cfg := DefaultConfig()
	cfg.MinSyncIntervalSeconds = 60

	if e.inCooldown(1, cfg) {
		t.Fatal("未同步过不应在冷却期")
	}
	e.markSynced(1)
	if !e.inCooldown(1, cfg) {
		t.Fatal("刚同步过应在冷却期")
	}
	cfg.MinSyncIntervalSeconds = 0
	if e.inCooldown(1, cfg) {
		t.Fatal("冷却关闭后不应拦截")
	}
}
