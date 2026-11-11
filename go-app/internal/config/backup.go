package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// 与 Python 版 app/backup.py 完全一致的参数，两边可互相导入导出。
const (
	BackupIterations = 200_000
	backupKeyLen     = 32
	backupSaltLen    = 16
)

// pbkdf2Key 是 PBKDF2-HMAC-SHA256（Go 标准库未直接提供）。
func pbkdf2Key(password string, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	u := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		_, _ = prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		_, _ = prf.Write(buf[:])
		u = prf.Sum(u[:0])
		t := make([]byte, hashLen)
		copy(t, u)
		for n := 1; n < iter; n++ {
			prf.Reset()
			_, _ = prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// EncryptBackup 把备份数据加密成可安全传输的 JSON 结构。
func EncryptBackup(data map[string]any, password string) (map[string]any, error) {
	plain, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := pbkdf2Key(password, salt, BackupIterations, backupKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	payload := make([]byte, 0, len(nonce)+len(ct))
	payload = append(payload, nonce...)
	payload = append(payload, ct...)
	return map[string]any{
		"encrypted":  true,
		"app":        "github-multi-account",
		"salt":       base64.StdEncoding.EncodeToString(salt),
		"iterations": BackupIterations,
		"payload":    base64.StdEncoding.EncodeToString(payload),
	}, nil
}

// DecryptBackup 解密备份。
func DecryptBackup(payload map[string]any, password string) (map[string]any, error) {
	if password == "" {
		return nil, errors.New("该备份已加密，请提供密码")
	}
	saltStr, _ := payload["salt"].(string)
	payloadStr, _ := payload["payload"].(string)
	if saltStr == "" || payloadStr == "" {
		return nil, errors.New("解密失败：备份文件缺少必要字段")
	}
	salt, err := base64.StdEncoding.DecodeString(saltStr)
	if err != nil {
		return nil, errors.New("解密失败：备份文件已损坏")
	}
	iter := BackupIterations
	if v, ok := payload["iterations"].(float64); ok && int(v) > 0 {
		iter = int(v)
	}
	raw, err := base64.StdEncoding.DecodeString(payloadStr)
	if err != nil {
		return nil, errors.New("解密失败：备份文件已损坏")
	}
	key := pbkdf2Key(password, salt, iter, backupKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("解密失败：备份文件已损坏")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("解密失败：密码错误或备份文件已损坏")
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("解密失败：%w", err)
	}
	return out, nil
}
