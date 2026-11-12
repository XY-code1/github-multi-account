// 全局聚合：账号总览、统一待办、最近活动。
//
// 所有数据都来自本地缓存（summaries / sync_state / rate_limits），
// 不实时请求 GitHub —— 几百个账号时不会打爆 API。
package ops

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github-multi-account/internal/config"
)

// Summary 是账号总览统计。
type Summary struct {
	Total          int    `json:"total"`
	Healthy        int    `json:"healthy"`
	Attention      int    `json:"attention"`
	CredentialRisk int    `json:"credential_risk"`
	QuotaLow       int    `json:"quota_low"`
	ReviewPRs      int    `json:"review_prs"`
	MyPRs          int    `json:"my_prs"`
	Issues         int    `json:"issues"`
	LastSyncAt     string `json:"last_sync_at"`
	SyncedAccounts int    `json:"synced_accounts"`
	GeneratedAt    string `json:"generated_at"`
}

// Task 是一条统一待办。
type Task struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	TypeLabel     string `json:"type_label"`
	Priority      string `json:"priority"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	AccountID     int    `json:"account_id"`
	AccountLabel  string `json:"account_label"`
	Time          string `json:"time"`
	Action        string `json:"action"`
}

// Activity 是一条跨账号动态。
type Activity struct {
	AccountID    int    `json:"account_id"`
	AccountLabel string `json:"account_label"`
	Type         string `json:"type"`
	Actor        string `json:"actor"`
	ActorAvatar  string `json:"actor_avatar"`
	Repo         string `json:"repo"`
	Action       string `json:"action"`
	CreatedAt    string `json:"created_at"`
}

var taskTypeLabels = map[string]string{
	"credential":  "凭据异常",
	"quota":       "API 配额不足",
	"review_pr":   "待我 Review 的 PR",
	"my_pr":       "我发起的 PR",
	"issue":       "分配给我的 Issue",
	"sync_failed": "同步失败",
	"sync_stale":  "长期未同步",
}

var priorityOrder = map[string]int{"high": 0, "medium": 1, "low": 2}

const quotaLowThreshold = 100
const staleAfter = 6 * time.Hour

// BuildSummary 汇总账号总览。
func BuildSummary(db *sql.DB, accounts []config.Account) (*Summary, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	states, err := AllSyncStates(db)
	if err != nil {
		return nil, err
	}
	limits, err := AllRateLimits(db)
	if err != nil {
		return nil, err
	}
	sm := make(map[int]SummaryRow, len(summaries))
	for _, s := range summaries {
		sm[s.AccountID] = s
	}
	tm := make(map[int]SyncState, len(states))
	for _, s := range states {
		tm[s.AccountID] = s
	}

	out := &Summary{
		Total:          len(accounts),
		SyncedAccounts: len(summaries),
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	for _, a := range accounts {
		s, hasS := sm[a.ID]
		st := tm[a.ID]
		status := "unknown"
		if hasS {
			status = s.Status
		} else if st.LastStatus != "" {
			status = st.LastStatus
		}
		if status == "active" {
			out.Healthy++
		} else {
			out.Attention++
		}
		if status == "invalid" || status == "error" {
			out.CredentialRisk++
		}
		out.ReviewPRs += s.ReviewPRs
		out.MyPRs += s.MyPRs
		out.Issues += s.AssignedIssues
		if rl, ok := limits[a.ID]; ok && rl.Remaining < quotaLowThreshold {
			out.QuotaLow++
		}
		if st.LastSyncAt != "" && st.LastSyncAt > out.LastSyncAt {
			out.LastSyncAt = st.LastSyncAt
		}
	}
	return out, nil
}

// BuildTasks 按优先级聚合所有账号的待处理事项。
func BuildTasks(db *sql.DB, accounts []config.Account) ([]Task, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	states, err := AllSyncStates(db)
	if err != nil {
		return nil, err
	}
	limits, err := AllRateLimits(db)
	if err != nil {
		return nil, err
	}
	accs := make(map[int]config.Account, len(accounts))
	for _, a := range accounts {
		accs[a.ID] = a
	}

	tasks := []Task{}
	for _, s := range summaries {
		acc := accs[s.AccountID]
		label := s.Label
		if label == "" {
			label = acc.Label
		}
		if label == "" {
			label = acc.Username
		}
		if label == "" {
			label = "账号 " + itoa(s.AccountID)
		}
		st := statesByIndex(states, s.AccountID)
		rl, hasRL := limits[s.AccountID]
		syncedAt := st.LastSyncAt

		if s.Status == "invalid" || s.Status == "error" {
			tasks = append(tasks, newTask("credential", "high", "凭据无效或已过期",
				orString(s.Error, "登录失败，请检查 Token"), label, s.AccountID, syncedAt, "重新录入凭据"))
		}
		if hasRL && rl.Remaining < quotaLowThreshold {
			tasks = append(tasks, newTask("quota", "medium", "API 配额不足",
				itoa(rl.Remaining)+"/"+itoa(rl.QuotaLimit), label, s.AccountID, rl.UpdatedAt, "查看配额"))
		}
		if s.ReviewPRs > 0 {
			tasks = append(tasks, newTask("review_pr", "medium",
				itoa(s.ReviewPRs)+" 个 PR 等待你 Review", "有人请求你审查代码",
				label, s.AccountID, syncedAt, "去 Review"))
		}
		if s.MyPRs > 0 {
			tasks = append(tasks, newTask("my_pr", "low",
				itoa(s.MyPRs)+" 个我发起的 PR 未关闭", "关注 CI 与评审进展",
				label, s.AccountID, syncedAt, "查看"))
		}
		if s.AssignedIssues > 0 {
			tasks = append(tasks, newTask("issue", "medium",
				itoa(s.AssignedIssues)+" 个 Issue 待处理", "分配给你的未关闭 Issue",
				label, s.AccountID, syncedAt, "去处理"))
		}
		if st.ConsecutiveFailures >= 3 {
			tasks = append(tasks, newTask("sync_failed", "high", "连续多次同步失败",
				orString(st.LastError, "请检查网络与凭据"), label, s.AccountID, st.UpdatedAt, "立即重试"))
		}
		if syncedAt != "" {
			if t, err := time.Parse(time.RFC3339, syncedAt); err == nil {
				if time.Since(t) > staleAfter {
					tasks = append(tasks, newTask("sync_stale", "low", "超过 6 小时未同步",
						"上次同步 "+syncedAt, label, s.AccountID, syncedAt, "立即同步"))
				}
			}
		}
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		pi := priorityOrder[tasks[i].Priority]
		pj := priorityOrder[tasks[j].Priority]
		if pi != pj {
			return pi < pj
		}
		return tasks[i].Time < tasks[j].Time
	})
	return tasks, nil
}

// BuildActivities 跨账号汇总最近动态（来自缓存的 events）。
func BuildActivities(db *sql.DB, limit int, typeFilter string) ([]Activity, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	out := []Activity{}
	for _, s := range summaries {
		var payload struct {
			Events []struct {
				Type      string `json:"type"`
				Actor     struct {
					Login     string `json:"login"`
					AvatarURL string `json:"avatar_url"`
				} `json:"actor"`
				Repo struct {
					Name string `json:"name"`
				} `json:"repo"`
				Payload   map[string]any `json:"payload"`
				CreatedAt string         `json:"created_at"`
			} `json:"events"`
		}
		if len(s.Payload) == 0 {
			continue
		}
		if err := json.Unmarshal(s.Payload, &payload); err != nil {
			continue
		}
		label := s.Label
		if label == "" {
			label = s.Username
		}
		for _, ev := range payload.Events {
			if typeFilter == "pr" && !strings.Contains(ev.Type, "PullRequest") {
				continue
			}
			if typeFilter == "issue" && !strings.Contains(ev.Type, "Issue") {
				continue
			}
			action, _ := ev.Payload["action"].(string)
			out = append(out, Activity{
				AccountID:    s.AccountID,
				AccountLabel: label,
				Type:         ev.Type,
				Actor:        ev.Actor.Login,
				ActorAvatar:  ev.Actor.AvatarURL,
				Repo:         ev.Repo.Name,
				Action:       action,
				CreatedAt:    ev.CreatedAt,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newTask(type_, priority, title, detail, label string, accountID int, t, action string) Task {
	return Task{
		ID:           type_ + ":" + itoa(accountID),
		Type:         type_,
		TypeLabel:    taskTypeLabels[type_],
		Priority:     priority,
		Title:        title,
		Detail:       detail,
		AccountID:    accountID,
		AccountLabel: label,
		Time:         t,
		Action:       action,
	}
}

func statesByIndex(states []SyncState, id int) SyncState {
	for _, s := range states {
		if s.AccountID == id {
			return s
		}
	}
	return SyncState{}
}

func orString(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
