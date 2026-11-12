// Package notify 实现外部通知渠道：Bark / Server酱 / 企业微信 / 通用 Webhook。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github-multi-account/internal/config"
	"github-multi-account/internal/service"
)

// ChannelTypes 是支持的渠道类型。
var ChannelTypes = []string{"bark", "webhook", "serverchan", "wecom"}

// Channel 是一个通知渠道。
type Channel struct {
	Type    string `yaml:"type" json:"type"`
	Name    string `yaml:"name" json:"name"`
	URL     string `yaml:"url" json:"url"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type settingsFile struct {
	Channels []Channel `yaml:"channels"`
}

func settingsPath(baseDir string) string {
	return filepath.Join(baseDir, "config", "settings.yaml")
}

// LoadChannels 读取渠道配置。
func LoadChannels(baseDir string) []Channel {
	data, err := os.ReadFile(settingsPath(baseDir))
	if err != nil {
		return nil
	}
	var f settingsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.Channels
}

// SaveChannels 保存渠道配置（保留同步设置等其他分节，不整份覆盖）。
func SaveChannels(baseDir string, channels []Channel) error {
	dir := filepath.Join(baseDir, "config")
	data := config.ReadSettings(dir)
	data["channels"] = channels
	return config.WriteSettings(dir, data)
}

// SendChannel 向单个渠道推送。
func SendChannel(ch Channel, title, body, link string) bool {
	if !ch.Enabled || ch.URL == "" {
		return false
	}
	client := &http.Client{Timeout: 10 * time.Second}
	postJSON := func(v any) bool {
		b, _ := json.Marshal(v)
		resp, err := client.Post(ch.URL, "application/json", bytes.NewReader(b))
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode < 400
	}
	switch ch.Type {
	case "webhook":
		return postJSON(map[string]any{"title": title, "body": body, "url": link})
	case "bark":
		return postJSON(map[string]any{"title": title, "body": body})
	case "serverchan":
		u := fmt.Sprintf("%s?title=%s&desp=%s", ch.URL, url.QueryEscape(title), url.QueryEscape(body))
		resp, err := client.Get(u)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode < 400
	case "wecom":
		content := title + "\n" + body
		return postJSON(map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": content},
		})
	}
	return false
}

// SendAll 推送到所有启用渠道，返回成功数。
func SendAll(channels []Channel, title, body, link string) int {
	count := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		wg.Add(1)
		go func(ch Channel) {
			defer wg.Done()
			if SendChannel(ch, title, body, link) {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}(ch)
	}
	wg.Wait()
	return count
}

// Poller 后台轮询 PR/Issue 数量变化并推送（首次只建立基线）。
type Poller struct {
	Interval time.Duration
	baseDir  string
	cfg      *config.Store
	svc      *service.Service

	mu       sync.Mutex
	previous map[int]map[string]int
	first    bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewPoller(baseDir string, interval time.Duration, cfg *config.Store, svc *service.Service) *Poller {
	return &Poller{
		Interval: interval,
		baseDir:  baseDir,
		cfg:      cfg,
		svc:      svc,
		previous: map[int]map[string]int{},
		first:    true,
		stop:     make(chan struct{}),
	}
}

func (p *Poller) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.Tick()
			case <-p.stop:
				return
			}
		}
	}()
}

func (p *Poller) Stop() {
	close(p.stop)
	p.wg.Wait()
}

// IsRunning 报告轮询是否已启动。
func (p *Poller) IsRunning() bool {
	select {
	case <-p.stop:
		return false
	default:
		return true
	}
}

// Tick 检查一次变化并推送。
func (p *Poller) Tick() {
	accounts, err := p.cfg.Accounts()
	if err != nil || len(accounts) == 0 {
		return
	}
	summaries := p.svc.Summaries(accounts)

	p.mu.Lock()
	defer p.mu.Unlock()

	messages := []string{}
	for _, s := range summaries {
		prev := p.previous[s.ID]
		if prev == nil {
			prev = map[string]int{"review": 0, "issues": 0}
		}
		review := s.ReviewPRsCount
		issues := s.AssignedIssuesCount
		if !p.first && (review != prev["review"] || issues != prev["issues"]) {
			parts := []string{}
			if review > prev["review"] {
				parts = append(parts, fmt.Sprintf("待处理 PR +%d", review-prev["review"]))
			}
			if issues > prev["issues"] {
				parts = append(parts, fmt.Sprintf("分配 Issue +%d", issues-prev["issues"]))
			}
			if len(parts) > 0 {
				messages = append(messages, fmt.Sprintf("%s: %s", s.Label, join(parts, "，")))
			}
		}
		p.previous[s.ID] = map[string]int{"review": review, "issues": issues}
	}
	p.first = false

	if len(messages) > 0 {
		SendAll(LoadChannels(p.baseDir), "GitHub 账号变化", join(messages, "；"), "")
	}
}

func join(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
