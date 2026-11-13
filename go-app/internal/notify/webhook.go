package notify

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github-multi-account/internal/config"
)

// Webhook 是一个出站 Webhook 配置。
type Webhook struct {
	Name    string   `yaml:"name" json:"name"`
	URL     string   `yaml:"url" json:"url"`
	Events  []string `yaml:"events" json:"events"`
	Enabled bool     `yaml:"enabled" json:"enabled"`
}

// Delivery 是一次 Webhook 投递的结果。
type Delivery struct {
	URL       string
	EventType string
	OK        bool
	Detail    string
}

// WebhookEventTypes 是支持的事件类型。
var WebhookEventTypes = []string{
	"review_pr.added",
	"my_pr.added",
	"issue.added",
	"account.invalid",
	"account.capped",
	"sync.failed",
}

// LoadWebhooks 读取出站 Webhook 配置。
func LoadWebhooks(configDir string) []Webhook {
	data := config.ReadSettings(configDir)
	raw, ok := data["webhooks"].([]any)
	if !ok {
		return nil
	}
	out := make([]Webhook, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		events := []string{}
		if list, ok := m["events"].([]any); ok {
			for _, e := range list {
				if s, ok := e.(string); ok && s != "" {
					events = append(events, s)
				}
			}
		}
		enabled := true
		if v, ok := m["enabled"].(bool); ok {
			enabled = v
		}
		out = append(out, Webhook{
			Name:    str(m["name"]),
			URL:     str(m["url"]),
			Events:  events,
			Enabled: enabled,
		})
	}
	return out
}

// SaveWebhooks 保存出站 Webhook 配置（保留其他分节）。
func SaveWebhooks(configDir string, hooks []Webhook) error {
	data := config.ReadSettings(configDir)
	if hooks == nil {
		hooks = []Webhook{}
	}
	data["webhooks"] = hooks
	return config.WriteSettings(configDir, data)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Dispatch 把事件推送到所有订阅它的 Webhook，返回每次投递的结果。
func Dispatch(configDir, eventType string, payload map[string]any) []Delivery {
	out := []Delivery{}
	for _, hook := range LoadWebhooks(configDir) {
		if !hook.Enabled || hook.URL == "" {
			continue
		}
		if len(hook.Events) > 0 && !containsString(hook.Events, eventType) {
			continue
		}
		body, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Post(hook.URL, "application/json", bytes.NewReader(body))
		d := Delivery{URL: hook.URL, EventType: eventType}
		if err != nil {
			d.Detail = err.Error()
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				d.Detail = http.StatusText(resp.StatusCode)
			} else {
				d.OK = true
			}
		}
		out = append(out, d)
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
