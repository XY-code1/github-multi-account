package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SettingsPath 返回配置目录下 settings.yaml 的路径。
func SettingsPath(configDir string) string {
	return filepath.Join(configDir, "settings.yaml")
}

// ReadSettings 读取 settings.yaml 的原始内容（缺失或损坏时返回空 map）。
//
// 通知渠道与同步设置共用这一个文件，任何一方保存时都必须先读取再合并，
// 否则会把另一方的配置整份覆盖掉。
func ReadSettings(configDir string) map[string]any {
	data, err := os.ReadFile(SettingsPath(configDir))
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// WriteSettings 把整份配置写回 settings.yaml（保留文件中其他分节）。
func WriteSettings(configDir string, data map[string]any) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(SettingsPath(configDir), b, 0o600)
}
