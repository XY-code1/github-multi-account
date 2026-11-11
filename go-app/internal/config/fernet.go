// Package config 实现 Fernet 兼容的 Token 加解密。
//
// 与 Python cryptography.Fernet 完全兼容：32 字节密钥，前 16 字节做 HMAC，
// 后 16 字节做 AES-128-CBC；token 为 base64url(version||ts||iv||ct||hmac)。
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

const fernetVersion = 0x80

var ErrNotValid = errors.New("fernet: invalid token")

// Key 是 32 字节的 Fernet 密钥。
type Key []byte

func NewKey() (Key, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return Key(k), nil
}

func (k Key) Encode() string { return base64.URLEncoding.EncodeToString(k) }

func ParseKey(s string) (Key, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("fernet: key must be 32 bytes")
	}
	return Key(b), nil
}

// Encrypt 加密明文，返回 base64url token。
func (k Key) Encrypt(plain []byte) (string, error) {
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k[16:])
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	header := make([]byte, 1+8+len(iv)+len(ct))
	header[0] = fernetVersion
	binary.BigEndian.PutUint64(header[1:9], uint64(time.Now().Unix()))
	copy(header[9:], iv)
	copy(header[9+len(iv):], ct)

	mac := hmac.New(sha256.New, k[:16])
	mac.Write(header)
	return base64.URLEncoding.EncodeToString(append(header, mac.Sum(nil)...)), nil
}

// Decrypt 解密 token。
func (k Key) Decrypt(token string) ([]byte, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrNotValid
	}
	// version(1) + timestamp(8) + iv(16) + ciphertext(>=16) + hmac(32)
	if len(raw) < 1+8+16+16+32 || raw[0] != fernetVersion {
		return nil, ErrNotValid
	}
	iv := raw[9 : 9+16]
	ct := raw[9+16 : len(raw)-32]
	want := raw[len(raw)-32:]

	mac := hmac.New(sha256.New, k[:16])
	mac.Write(raw[:len(raw)-32])
	if !hmac.Equal(mac.Sum(nil), want) {
		return nil, ErrNotValid
	}

	block, err := aes.NewCipher(k[16:])
	if err != nil {
		return nil, err
	}
	if len(ct)%aes.BlockSize != 0 {
		return nil, ErrNotValid
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	return pkcs7Unpad(pt, aes.BlockSize)
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	out := make([]byte, len(b)+n)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(b []byte, size int) ([]byte, error) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, ErrNotValid
	}
	n := int(b[len(b)-1])
	if n == 0 || n > size || n > len(b) {
		return nil, ErrNotValid
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, ErrNotValid
		}
	}
	return b[:len(b)-n], nil
}
