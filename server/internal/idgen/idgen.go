// Package idgen 统一生成各类随机标识。
//
// 背景：此前 store / api / auth / sigauth / callback / auditx 六个包各自实现了
// 一份等价的随机 ID 生成（其中 5 份逐行相同），修改算法或加长随机位需要改 6 处，
// 且极易漏改导致同类 ID 强度不一致。此处收敛为唯一实现。
package idgen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

// Hex 返回 n 字节加密随机数的十六进制表示（长度 2n）。
func Hex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithPrefix 返回 prefix + Hex(n)，用于带类型前缀的实体 ID（如 cbl_ / al_）。
func WithPrefix(prefix string, n int) string {
	return prefix + Hex(n)
}

// B64 返回 n 字节随机数的 URL-safe base64（无填充）。
// 用于 OIDC state 与 PKCE code_verifier。
func B64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
