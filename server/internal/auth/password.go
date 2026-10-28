// Package auth — password.go：argon2id 密码哈希与规则校验。
// 参数（契约 §4.2）：time=2、memory=64MiB、threads=4、keyLen=32、salt 16 字节，PHC 编码串。
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword 生成 argon2id PHC 串：$argon2id$v=19$m=...,t=...,p=...$salt$hash
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword 校验密码是否匹配 PHC 串（常量时间）。
func VerifyPassword(pw, phc string) (bool, error) {
	parts := strings.Split(phc, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid password hash format")
	}
	var memory, timeI, threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeI, &threads); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(pw), salt, timeI, memory, uint8(threads), uint32(len(want)))
	return subtleEqual(got, want), nil
}

func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ValidatePasswordRule 校验密码规则：≥8 位且同时含字母和数字。返回空串表示通过。
func ValidatePasswordRule(pw string) string {
	if len(pw) < 8 {
		return "密码至少 8 位"
	}
	var hasLetter, hasDigit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return "密码必须同时包含字母和数字"
	}
	return ""
}

// ValidateUsername 校验用户名规则：3-64 位，字母数字下划线连字符。
func ValidateUsername(name string) string {
	if l := len([]rune(name)); l < 3 || l > 64 {
		return "用户名长度需 3-64 位"
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return "用户名只能含字母、数字、下划线、连字符"
		}
	}
	return ""
}
