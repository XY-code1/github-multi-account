package docx

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func makeDocx(t *testing.T, lines []string) []byte {
	t.Helper()
	var body bytes.Buffer
	for _, line := range lines {
		body.WriteString(`<w:p><w:r><w:t>` + line + `</w:t></w:r></w:p>`)
	}
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("创建条目失败: %v", err)
	}
	if _, err := w.Write([]byte(xml)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func TestExtractLines(t *testing.T) {
	lines, err := ExtractLines(makeDocx(t, []string{"第一段", "第二段", ""}))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(lines) != 2 || lines[0] != "第一段" || lines[1] != "第二段" {
		t.Fatalf("段落提取不正确: %#v", lines)
	}
}

func TestExtractLinesInvalid(t *testing.T) {
	if _, err := ExtractLines([]byte("not a zip")); err == nil {
		t.Fatal("非 zip 数据应报错")
	}
}

func TestFindCandidatesWithLabel(t *testing.T) {
	data := makeDocx(t, []string{
		"账号：alice",
		"Token: ghp_" + strings.Repeat("a", 36),
		"用户名: bob",
		"ghp_" + strings.Repeat("b", 36),
	})
	found, err := FindCandidates(data)
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("应识别出 2 个 Token: %#v", found)
	}
	if found[0].Username != "alice" || !strings.HasPrefix(found[0].Token, "ghp_") {
		t.Fatalf("第一条识别不正确: %#v", found[0])
	}
	if found[1].Username != "bob" {
		t.Fatalf("第二条识别不正确: %#v", found[1])
	}
}

func TestFindCandidatesPrevLineBareUsername(t *testing.T) {
	found, err := FindCandidates(makeDocx(t, []string{"carol", "ghp_" + strings.Repeat("c", 36)}))
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if len(found) != 1 || found[0].Username != "carol" {
		t.Fatalf("上一行裸用户名识别不正确: %#v", found)
	}
}

func TestFindCandidatesDedup(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	found, err := FindCandidates(makeDocx(t, []string{token, token}))
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("重复 Token 应去重: %#v", found)
	}
}

func TestFindCandidatesFineGrained(t *testing.T) {
	token := "github_pat_" + strings.Repeat("a", 22) + "_" + strings.Repeat("b", 20)
	found, err := FindCandidates(makeDocx(t, []string{"dave", token}))
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if len(found) != 1 || found[0].Token != token || found[0].Username != "dave" {
		t.Fatalf("细粒度 Token 识别不正确: %#v", found)
	}
}

func TestBuildCandidatesMasksToken(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	c := BuildCandidates([]Found{{Username: "alice", Token: token}}, func(s string) string {
		return s[:4] + "****"
	})[0]
	if c.TokenMasked != "ghp_****" {
		t.Fatalf("脱敏不正确: %q", c.TokenMasked)
	}
	if strings.Contains(c.TokenMasked, "aaaa") {
		t.Fatalf("脱敏后仍含明文: %q", c.TokenMasked)
	}
	if c.Token() != token {
		t.Fatalf("服务端应保留明文 Token 供写入: %q", c.Token())
	}
}
