// Package config 管理账号配置：加密存储、迁移、导入导出。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Account 是一个账号。Token 永不序列化到 YAML（只写密文）。
type Account struct {
	ID       int
	Label    string
	Username string
	Token    string
}

// MaskedToken 返回脱敏 Token。
func (a Account) MaskedToken() string {
	t := a.Token
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "********"
	}
	return t[:4] + "********" + t[len(t)-4:]
}

type rawAccount struct {
	Label          string `yaml:"label"`
	Username       string `yaml:"username"`
	Token          string `yaml:"token"`
	TokenEncrypted string `yaml:"token_encrypted"`
}

type fileFormat struct {
	Accounts []rawAccount `yaml:"accounts"`
}

// Store 是线程安全的账号配置存储。
type Store struct {
	dir      string
	mu       sync.Mutex
	key      Key
	mtime    int64
	accounts []Account
}

// Open 打开（或初始化）配置目录与本机密钥。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if err := s.loadKey(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) loadKey() error {
	path := filepath.Join(s.dir, ".secret.key")
	data, err := os.ReadFile(path)
	if err == nil {
		k, err := ParseKey(string(data))
		if err != nil {
			return fmt.Errorf("密钥文件无效: %w", err)
		}
		s.key = k
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	k, err := NewKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(k.Encode()), 0o600); err != nil {
		return err
	}
	s.key = k
	return nil
}

func (s *Store) accountsPath() string { return filepath.Join(s.dir, "accounts.yaml") }

// Accounts 返回全部账号（带内存缓存，文件 mtime 变化时失效）。
func (s *Store) Accounts() ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(s.accountsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if s.accounts != nil && s.mtime == info.ModTime().UnixNano() {
		return clone(s.accounts), nil
	}

	data, err := os.ReadFile(s.accountsPath())
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("accounts.yaml 解析失败，文件可能已损坏: %w", err)
	}
	if f.Accounts == nil {
		f.Accounts = []rawAccount{}
	}

	hasEncrypted, plain := false, false
	for _, a := range f.Accounts {
		if a.TokenEncrypted != "" {
			hasEncrypted = true
		}
		if a.Token != "" && a.TokenEncrypted == "" {
			plain = true
		}
	}
	if hasEncrypted && s.key == nil {
		return nil, errors.New("检测到已加密的 Token，但密钥文件 config/.secret.key 缺失，无法解密。请恢复密钥文件，或手动清空 accounts.yaml 后重新录入账号。")
	}

	accounts := make([]Account, 0, len(f.Accounts))
	for i, a := range f.Accounts {
		token := a.Token
		if a.TokenEncrypted != "" {
			t, err := s.key.Decrypt(a.TokenEncrypted)
			if err != nil {
				return nil, errors.New("Token 解密失败：.secret.key 与 accounts.yaml 不匹配（密钥可能被替换）。请恢复原始密钥文件。")
			}
			token = string(t)
		}
		accounts = append(accounts, Account{
			ID:       i + 1,
			Label:    a.Label,
			Username: a.Username,
			Token:    token,
		})
	}

	if plain {
		if err := s.writeLocked(accounts); err != nil {
			return nil, err
		}
	}

	s.accounts = accounts
	s.mtime = info.ModTime().UnixNano()
	if plain {
		if fi, err := os.Stat(s.accountsPath()); err == nil {
			s.mtime = fi.ModTime().UnixNano()
		}
	}
	return clone(accounts), nil
}

func (s *Store) writeLocked(accounts []Account) error {
	f := fileFormat{Accounts: make([]rawAccount, 0, len(accounts))}
	for _, a := range accounts {
		enc, err := s.key.Encrypt([]byte(a.Token))
		if err != nil {
			return err
		}
		f.Accounts = append(f.Accounts, rawAccount{
			Label:          a.Label,
			Username:       a.Username,
			TokenEncrypted: enc,
		})
	}
	data, err := yaml.Marshal(&f)
	if err != nil {
		return err
	}
	path := s.accountsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	s.accounts = nil
	s.mtime = 0
	return nil
}

// Save 全量写回。
func (s *Store) Save(accounts []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(accounts)
}

// Add 新增或更新（同 username 覆盖）。
func (s *Store) Add(label, username, token string) (Account, error) {
	accounts, err := s.Accounts()
	if err != nil {
		return Account{}, err
	}
	for i := range accounts {
		if username != "" && accounts[i].Username == username {
			accounts[i].Label = label
			accounts[i].Token = token
			if err := s.Save(accounts); err != nil {
				return Account{}, err
			}
			return accounts[i], nil
		}
	}
	acc := Account{Label: label, Username: username, Token: token}
	accounts = append(accounts, acc)
	for i := range accounts {
		accounts[i].ID = i + 1
	}
	if err := s.Save(accounts); err != nil {
		return Account{}, err
	}
	return accounts[len(accounts)-1], nil
}

// Delete 删除账号，返回被删账号的 username（用于清理历史）。
func (s *Store) Delete(id int) (string, bool, error) {
	accounts, err := s.Accounts()
	if err != nil {
		return "", false, err
	}
	var username string
	found := false
	kept := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if a.ID == id {
			username = a.Username
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return "", false, nil
	}
	for i := range kept {
		kept[i].ID = i + 1
	}
	if err := s.Save(kept); err != nil {
		return "", false, err
	}
	return username, true, nil
}

// Export 导出明文备份。
func (s *Store) Export() (map[string]any, error) {
	accounts, err := s.Accounts()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, map[string]any{
			"label":    a.Label,
			"username": a.Username,
			"token":    a.Token,
		})
	}
	return map[string]any{
		"version":     1,
		"app":         "github-multi-account",
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"accounts":    out,
	}, nil
}

// Import 从备份导入（同 username 覆盖）。
func (s *Store) Import(payload map[string]any) (added, updated, skipped int, err error) {
	raw, ok := payload["accounts"]
	if !ok {
		return 0, 0, 0, errors.New("备份格式不正确：缺少 accounts 字段")
	}
	items, ok := raw.([]any)
	if !ok {
		return 0, 0, 0, errors.New("备份格式不正确：accounts 应为列表")
	}
	accounts, err := s.Accounts()
	if err != nil {
		return 0, 0, 0, err
	}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			skipped++
			continue
		}
		username, _ := m["username"].(string)
		token, _ := m["token"].(string)
		if username == "" || token == "" {
			skipped++
			continue
		}
		label, _ := m["label"].(string)
		if label == "" {
			label = username
		}
		found := false
		for i := range accounts {
			if accounts[i].Username == username {
				accounts[i].Label = label
				accounts[i].Token = token
				updated++
				found = true
				break
			}
		}
		if !found {
			accounts = append(accounts, Account{Label: label, Username: username, Token: token})
			added++
		}
	}
	if added > 0 || updated > 0 {
		for i := range accounts {
			accounts[i].ID = i + 1
		}
		if err := s.Save(accounts); err != nil {
			return 0, 0, 0, err
		}
	}
	return added, updated, skipped, nil
}

func clone(in []Account) []Account {
	out := make([]Account, len(in))
	copy(out, in)
	return out
}
