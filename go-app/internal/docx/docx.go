// Package docx 从 .docx 文档中识别 GitHub Token 与用户名。
//
// .docx 本质是一个 zip 包，正文在 word/document.xml。这里不引入第三方依赖，
// 直接解压 + 解析 XML，按段落（<w:p>）提取文本，表格单元格同样按段落处理。
//
// Token 形态：
//   - Classic PAT：ghp_ / gho_ / ghu_ / ghs_ / ghr_ + 36 位以上字母数字
//   - Fine-grained PAT：github_pat_ + 下划线与字母数字
//
// 用户名的权威来源是 GitHub /user 接口（用 Token 反查 login），文档里识别到的
// 用户名只作为无效 Token 的兜底显示名。
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
)

var (
	tokenRe   = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}`)
	labelRe   = regexp.MustCompile(`(?i)(?:用户名|账号|登录名|登陆名|login|username|user|name)\s*[:：]\s*([A-Za-z0-9][A-Za-z0-9-]{0,37})`)
	mentionRe = regexp.MustCompile(`@([A-Za-z0-9][A-Za-z0-9-]{0,37})`)
	wordRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,37}$`)
	skipWords = map[string]bool{
		"token": true, "github": true, "pat": true,
		"ghp": true, "gho": true, "ghu": true, "ghs": true, "ghr": true,
		"用户名": true, "账号": true, "密码": true, "password": true,
		"secret": true, "key": true, "access": true,
	}
)

// Found 是文档里识别到的一条原始记录。
type Found struct {
	Username string
	Token    string
}

// Candidate 是一个待导入账号。Token 只在服务端使用，不对外返回。
type Candidate struct {
	Index       int    `json:"index"`
	Username    string `json:"username"`
	Label       string `json:"label"`
	TokenMasked string `json:"token_masked"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error"`

	token string
}

// Token 返回明文 Token（仅服务端使用）。
func (c Candidate) Token() string { return c.token }

// ExtractLines 按段落提取 .docx 文本。
func ExtractLines(data []byte) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var doc []byte
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		doc, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		break
	}
	if doc == nil {
		return nil, errors.New("不是有效的 .docx（缺少 word/document.xml）")
	}

	var lines []string
	var cur strings.Builder
	inText := false
	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				cur.Reset()
			case "t":
				inText = true
			}
		case xml.CharData:
			if inText {
				cur.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if line := strings.TrimSpace(cur.String()); line != "" {
					lines = append(lines, line)
				}
				cur.Reset()
			}
		}
	}
	return lines, nil
}

// FindCandidates 从文档字节中识别 Token 与上下文用户名，按出现顺序去重。
func FindCandidates(data []byte) ([]Found, error) {
	lines, err := ExtractLines(data)
	if err != nil {
		return nil, err
	}
	var found []Found
	seen := map[string]bool{}
	for idx, line := range lines {
		for _, token := range tokenRe.FindAllString(line, -1) {
			if seen[token] {
				continue
			}
			seen[token] = true
			found = append(found, Found{Username: usernameNear(lines, idx, line), Token: token})
		}
	}
	return found, nil
}

// BuildCandidates 把原始记录转成对外返回的候选（Token 脱敏）。
func BuildCandidates(found []Found, mask func(string) string) []Candidate {
	out := make([]Candidate, 0, len(found))
	for i, f := range found {
		out = append(out, Candidate{
			Index:       i,
			Username:    f.Username,
			Label:       f.Username,
			TokenMasked: mask(f.Token),
			token:       f.Token,
		})
	}
	return out
}

func usernameNear(lines []string, idx int, line string) string {
	if m := labelRe.FindStringSubmatch(line); len(m) > 1 {
		return m[1]
	}
	if m := mentionRe.FindStringSubmatch(line); len(m) > 1 {
		return m[1]
	}
	cleaned := tokenRe.ReplaceAllString(line, " ")
	if m := mentionRe.FindStringSubmatch(cleaned); len(m) > 1 {
		return m[1]
	}
	for _, w := range findAllWords(cleaned) {
		if !skipWords[strings.ToLower(w)] {
			return w
		}
	}
	for _, j := range []int{idx - 1, idx + 1} {
		if j < 0 || j >= len(lines) {
			continue
		}
		if m := labelRe.FindStringSubmatch(lines[j]); len(m) > 1 {
			return m[1]
		}
		if m := mentionRe.FindStringSubmatch(lines[j]); len(m) > 1 {
			return m[1]
		}
		if bare := strings.TrimSpace(lines[j]); wordRe.MatchString(bare) && !skipWords[strings.ToLower(bare)] {
			return bare
		}
	}
	return ""
}

// findAllWords 返回文本里所有可能的单词（不要求整行匹配）。
func findAllWords(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}
