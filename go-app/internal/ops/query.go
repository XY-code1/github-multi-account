// 账号列表：服务端搜索 / 筛选 / 排序 / 分页。
//
// 几百个账号时在内存中过滤已经足够快（亚毫秒级），但接口形态是服务端的：
// 只返回当前页数据，不把全部账号推给前端。
package ops

import (
	"database/sql"
	"sort"
	"strings"

	"github-multi-account/internal/config"
)

// AccountQuery 是账号列表的查询参数。
type AccountQuery struct {
	Page     int
	PageSize int
	Query    string
	Status   string
	Type     string
	Group    string
	Tags     string
	Sort     string
	Order    string
}

// AccountRow 是账号列表中的一行（配置 + 缓存状态合并后的视图）。
type AccountRow struct {
	ID                  int      `json:"id"`
	Username            string   `json:"username"`
	Label               string   `json:"label"`
	Alias               string   `json:"alias"`
	Note                string   `json:"note"`
	Type                string   `json:"type"`
	Group               string   `json:"group"`
	Tags                []string `json:"tags"`
	Status              string   `json:"status"`
	Error               string   `json:"error"`
	LastSyncAt          string   `json:"last_sync_at"`
	LastStatus          string   `json:"last_status"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	SyncEnabled         bool     `json:"sync_enabled"`
	Remaining           int      `json:"remaining"`
	QuotaLimit          int      `json:"quota_limit"`
	ReviewPRs           int      `json:"review_prs"`
	MyPRs               int      `json:"my_prs"`
	AssignedIssues      int      `json:"assigned_issues"`
	Followers           int      `json:"followers"`
	Following           int      `json:"following"`
	PublicRepos         int      `json:"public_repos"`
	TotalStars          int      `json:"total_stars"`
	Todos               int      `json:"todos"`
	TokenMasked         string   `json:"token_masked"`
}

var statusAliases = map[string]string{
	"healthy":     "active",
	"warning":     "warning",
	"invalid":     "invalid",
	"error":       "error",
	"sync_failed": "sync_failed",
}

var sortFields = map[string]string{
	"last_activity": "last_sync_at",
	"last_sync":     "last_sync_at",
	"quota":         "remaining",
	"todos":         "todos",
	"username":      "username",
	"created":       "id",
}

// AccountResult 是分页查询结果。
type AccountResult struct {
	Items      []AccountRow `json:"items"`
	Total      int          `json:"total"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
	TotalPages int          `json:"total_pages"`
	Groups     []string     `json:"groups"`
	Tags       []string     `json:"tags"`
}

// QueryAccounts 合并账号配置与缓存状态，做搜索 / 筛选 / 排序 / 分页。
func QueryAccounts(db *sql.DB, accounts []config.Account, q AccountQuery) (*AccountResult, error) {
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

	rows := make([]AccountRow, 0, len(accounts))
	for _, a := range accounts {
		s := sm[a.ID]
		st := tm[a.ID]
		rl, hasRL := limits[a.ID]

		status := s.Status
		if status == "" {
			status = st.LastStatus
		}
		if status == "" {
			status = "unknown"
		}
		if st.ConsecutiveFailures >= 3 && status == "active" {
			status = "sync_failed"
		}

		typed := a.Type
		if typed == "" {
			typed = "personal"
		}
		tags := a.Tags
		if tags == nil {
			tags = []string{}
		}
		enable := true
		if st.SyncEnabled == 0 && st.AccountID != 0 {
			enable = false
		}
		remaining, quotaLimit := 0, 0
		if hasRL {
			remaining, quotaLimit = rl.Remaining, rl.QuotaLimit
		}
		label := a.Label
		if label == "" {
			label = a.Username
		}
		errMsg := s.Error
		if errMsg == "" {
			errMsg = st.LastError
		}
		rows = append(rows, AccountRow{
			ID:                  a.ID,
			Username:            a.Username,
			Label:               label,
			Alias:               a.Alias,
			Note:                a.Note,
			Type:                typed,
			Group:               a.Group,
			Tags:                tags,
			Status:              status,
			Error:               errMsg,
			LastSyncAt:          st.LastSyncAt,
			LastStatus:          st.LastStatus,
			ConsecutiveFailures: st.ConsecutiveFailures,
			SyncEnabled:         enable,
			Remaining:           remaining,
			QuotaLimit:          quotaLimit,
			ReviewPRs:           s.ReviewPRs,
			MyPRs:               s.MyPRs,
			AssignedIssues:      s.AssignedIssues,
			Followers:           s.Followers,
			Following:           s.Following,
			PublicRepos:         s.PublicRepos,
			TotalStars:          s.TotalStars,
			Todos:               s.ReviewPRs + s.AssignedIssues,
			TokenMasked:         a.MaskedToken(),
		})
	}

	groups := []string{}
	seenGroups := map[string]bool{}
	tagSet := map[string]bool{}
	for _, r := range rows {
		if r.Group != "" && !seenGroups[r.Group] {
			seenGroups[r.Group] = true
			groups = append(groups, r.Group)
		}
		for _, t := range r.Tags {
			if t != "" {
				tagSet[t] = true
			}
		}
	}
	sort.Strings(groups)
	tagsAll := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tagsAll = append(tagsAll, t)
	}
	sort.Strings(tagsAll)

	// --- 搜索 ---
	if query := strings.ToLower(strings.TrimSpace(q.Query)); query != "" {
		filtered := rows[:0]
		for _, r := range rows {
			match := containsFold(r.Username, query) ||
				containsFold(r.Label, query) ||
				containsFold(r.Alias, query) ||
				containsFold(r.Note, query)
			if !match {
				for _, t := range r.Tags {
					if containsFold(t, query) {
						match = true
						break
					}
				}
			}
			if match {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	// --- 筛选 ---
	if q.Status != "" {
		want := statusAliases[q.Status]
		if want == "" {
			want = q.Status
		}
		rows = filterRows(rows, func(r AccountRow) bool { return r.Status == want })
	}
	if q.Type != "" {
		rows = filterRows(rows, func(r AccountRow) bool { return r.Type == q.Type })
	}
	if q.Group != "" {
		rows = filterRows(rows, func(r AccountRow) bool { return r.Group == q.Group })
	}
	if q.Tags != "" {
		wanted := map[string]bool{}
		for _, t := range strings.Split(q.Tags, ",") {
			if v := strings.TrimSpace(strings.ToLower(t)); v != "" {
				wanted[v] = true
			}
		}
		rows = filterRows(rows, func(r AccountRow) bool {
			for _, t := range r.Tags {
				if wanted[strings.ToLower(t)] {
					return true
				}
			}
			return false
		})
	}

	// --- 排序 ---
	key := sortFields[q.Sort]
	if key == "" {
		key = "last_sync_at"
	}
	desc := !strings.EqualFold(q.Order, "asc")
	sort.SliceStable(rows, func(i, j int) bool {
		return compareRows(rows[i], rows[j], key, desc)
	})

	total := len(rows)
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 200 {
		pageSize = 200
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	return &AccountResult{
		Items:      rows[start:end],
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: (total + pageSize - 1) / pageSize,
		Groups:     groups,
		Tags:       tagsAll,
	}, nil
}

func filterRows(rows []AccountRow, keep func(AccountRow) bool) []AccountRow {
	out := make([]AccountRow, 0, len(rows))
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func containsFold(s, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(s), lowerQuery)
}

// compareRows 返回 true 表示 a 应排在 b 前面。缺失值始终排在最后。
func compareRows(a, b AccountRow, key string, desc bool) bool {
	av, aok := rowValue(a, key)
	bv, bok := rowValue(b, key)
	if !aok && !bok {
		return false
	}
	if !aok {
		return false
	}
	if !bok {
		return true
	}
	if desc {
		return av > bv
	}
	return av < bv
}

// rowValue 把排序键统一成可比较的字符串：数字补零到 20 位，
// 保证字典序与数值序一致；时间戳本身是 ISO 字符串，可直接比较。
func rowValue(r AccountRow, key string) (string, bool) {
	switch key {
	case "last_sync_at":
		return r.LastSyncAt, r.LastSyncAt != ""
	case "remaining":
		return padInt(r.Remaining), true
	case "todos":
		return padInt(r.Todos), true
	case "username":
		return r.Username, true
	case "id":
		return padInt(r.ID), true
	default:
		return r.LastSyncAt, r.LastSyncAt != ""
	}
}

func padInt(v int) string {
	s := "00000000000000000000"
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	d := itoa(v)
	if len(d) >= len(s) {
		return neg + d
	}
	return neg + s[:len(s)-len(d)] + d
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
