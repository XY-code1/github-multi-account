package config

import (
	"testing"
)

func TestExportHidesEmailWithoutCredentials(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开配置失败: %v", err)
	}
	if _, err := st.Add(Account{Label: "邮箱号", Username: "exp-u", Token: "t", Email: "token-secret"}); err != nil {
		t.Fatalf("建账号失败: %v", err)
	}

	info, err := st.Export(false, "")
	if err != nil {
		t.Fatalf("导出账号信息失败: %v", err)
	}
	items := info["accounts"].([]map[string]any)
	if _, ok := items[0]["email"]; ok {
		t.Fatal("账号信息备份不应包含关联邮箱（可能是公开取件 token）")
	}

	full, err := st.Export(true, "")
	if err != nil {
		t.Fatalf("导出凭据失败: %v", err)
	}
	fullItems := full["accounts"].([]map[string]any)
	if fullItems[0]["email"] != "token-secret" {
		t.Fatal("凭据备份应包含关联邮箱")
	}

	if _, _, _, err := st.Import(full, ""); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	accounts, _ := st.Accounts()
	for _, a := range accounts {
		if a.Username == "exp-u" && a.Email != "token-secret" {
			t.Fatalf("导入后关联邮箱丢失: %+v", a)
		}
	}
}
