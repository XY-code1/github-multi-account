package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github-multi-account/internal/config"
	"github-multi-account/internal/docx"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
)

const docxVerifyConcurrency = 5

// handleImportDocx 解析上传的 .docx 并识别 Token / 用户名。
//
// preview=true 只返回预览（Token 脱敏，绝不回明文）；preview=false 时按
// indices 导入选中的候选。
func (s *Server) handleImportDocx(w http.ResponseWriter, r *http.Request) {
	preview := r.URL.Query().Get("preview") == "true"
	want := parseIndices(r.URL.Query().Get("indices"))

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无法解析上传文件"})
		return
	}
	fh, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "缺少文件"})
		return
	}
	defer func() { _ = fh.Close() }()

	data, err := io.ReadAll(fh)
	if err != nil || len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "文件为空"})
		return
	}

	found, err := docx.FindCandidates(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if len(found) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"candidates": []any{}, "total": 0, "message": "没有在文档里找到 GitHub Token",
		})
		return
	}

	all := docx.BuildCandidates(found, config.MaskToken)
	targets := all
	if !preview {
		targets = selectCandidates(all, want)
	}
	verified := s.verifyDocxCandidates(targets)

	if preview {
		writeJSON(w, http.StatusOK, map[string]any{
			"candidates": verified, "total": len(verified), "message": "",
		})
		return
	}

	overrides := parseOverrides(r.FormValue("overrides"))
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	existing := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		existing[a.Username] = true
	}

	added, updated, skipped := 0, 0, 0
	out := []map[string]any{}
	for _, c := range verified {
		username := c.Username
		label := c.Label
		if o, ok := overrides[c.Index]; ok {
			if o.Username != "" {
				username = o.Username
			}
			if o.Label != "" {
				label = o.Label
			}
		}
		if label == "" {
			label = username
		}
		// 无效 Token 默认不勾选；用户手动勾选时仍允许导入（与手动添加账号一致）
		if username == "" || c.Token() == "" {
			skipped++
			continue
		}
		if existing[username] {
			updated++
		} else {
			added++
		}
		acc, err := s.cfg.Add(config.Account{
			Label: label, Username: username, Token: c.Token(), Type: "personal",
		})
		if err != nil {
			skipped++
			continue
		}
		out = append(out, map[string]any{
			"id":           acc.ID,
			"username":     acc.Username,
			"label":        acc.Label,
			"token_masked": acc.MaskedToken(),
		})
	}
	ops.Audit(s.opsDB, "accounts.import_docx", nil, "",
		fmt.Sprintf("从 Word 导入：新增 %d，更新 %d，跳过 %d", added, updated, skipped))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": added, "updated": updated, "skipped": skipped, "accounts": out,
	})
}

// verifyDocxCandidates 并发验证 Token（并发上限 5），用 GitHub 返回的 login 作为权威用户名。
func (s *Server) verifyDocxCandidates(cands []docx.Candidate) []docx.Candidate {
	sem := make(chan struct{}, docxVerifyConcurrency)
	var wg sync.WaitGroup
	for i := range cands {
		wg.Add(1)
		go func(c *docx.Candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			status, user, msg := service.Verify(c.Username, c.Token())
			if status != "active" || user == nil {
				c.Valid, c.Error = false, msg
				return
			}
			if user.Login != "" {
				c.Username = user.Login
			}
			if c.Label == "" {
				c.Label = c.Username
			}
			c.Valid, c.Error = true, ""
		}(&cands[i])
	}
	wg.Wait()
	return cands
}

func selectCandidates(all []docx.Candidate, want map[int]bool) []docx.Candidate {
	if len(want) == 0 {
		return nil
	}
	out := []docx.Candidate{}
	for _, c := range all {
		if want[c.Index] {
			out = append(out, c)
		}
	}
	return out
}

func parseIndices(raw string) map[int]bool {
	out := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			out[n] = true
		}
	}
	return out
}

type docxOverride struct {
	Username string `json:"username"`
	Label    string `json:"label"`
}

func parseOverrides(raw string) map[int]docxOverride {
	out := map[int]docxOverride{}
	if raw == "" {
		return out
	}
	var m map[string]docxOverride
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		if n, err := strconv.Atoi(k); err == nil {
			out[n] = v
		}
	}
	return out
}
