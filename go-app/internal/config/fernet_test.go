package config

import (
	"os"
	"testing"
)

// 该向量由 Python cryptography.Fernet 生成：
//   key = Fernet.generate_key(); token = Fernet(key).encrypt(b"ghp_cross_language_test_token_12345")
const pyKey = "CUJHIQJjDYZZVRcFpUi7wfKM9M3IqPvFSogjbJZJNw4="
const pyToken = "gAAAAABq9D4Wx5jE8H4kztT1DjlwTy4YouCQHZyDcMuSx5FWvZbojavP4u_Iz_F2OExxNazxQXXAySQixGAFoTyFm6mRTlnDJO5qqpiZ2c2lJPesI4m-rO2hFXWXWiBZd1ycFd9B4FV-"

// Go 必须能解密 Python 生成的 Token（否则用户现有配置会失效）。
func TestDecryptPythonToken(t *testing.T) {
	k, err := ParseKey(pyKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Decrypt(pyToken)
	if err != nil {
		t.Fatalf("解密 Python token 失败: %v", err)
	}
	if string(got) != "ghp_cross_language_test_token_12345" {
		t.Fatalf("明文不匹配: %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	plain := "ghp_supersecret_round_trip"
	enc, err := k.Encrypt([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := k.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != plain {
		t.Fatalf("round trip 失败: %q", dec)
	}
}

// 输出一个 Go 加密的 token，供 Python 侧验证（WRITE_VECTOR=1 时启用）。
func TestEncryptForPython(t *testing.T) {
	if os.Getenv("WRITE_VECTOR") == "" {
		t.Skip("设置 WRITE_VECTOR=1 以生成供 Python 验证的 token")
	}
	k, err := ParseKey(pyKey)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k.Encrypt([]byte("ghp_go_to_python_98765"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/go_token.txt", []byte(enc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("已写入 /tmp/go_token.txt: %s", enc)
}
