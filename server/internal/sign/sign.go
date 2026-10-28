// Package sign 实现契约 §4.10 的 HMAC-SHA256 签名直链。
// payload = "d:<document_id>:<exp_unix>" | "j:<job_id>:<exp_unix>"
// token = b64url(payload) + "." + b64url(hmac_sha256(secret, payload))
package sign

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signer 用服务端密钥签发与校验下载直链。
type Signer struct {
	secret []byte
	ttl    time.Duration
}

// New 构造 Signer。secret 为空时由调用方先随机生成。
func New(secret string, ttl time.Duration) *Signer { return &Signer{secret: []byte(secret), ttl: ttl} }

// GenerateRandomSecret 生成 32 字节随机密钥（未配置 SIGNED_URL_SECRET 时用）。
func GenerateRandomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Signer) b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

// SignDocument 签发文档直链，返回 token 与过期时间。
func (s *Signer) SignDocument(docID string) (token string, exp time.Time) {
	return s.sign("d", docID)
}

// SignJob 签发任务 zip 直链。
func (s *Signer) SignJob(jobID string) (token string, exp time.Time) {
	return s.sign("j", jobID)
}

func (s *Signer) sign(kind, id string) (string, time.Time) {
	exp := time.Now().Add(s.ttl).Unix()
	payload := fmt.Sprintf("%s:%s:%d", kind, id, exp)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	token := s.b64([]byte(payload)) + "." + s.b64(mac.Sum(nil))
	return token, time.Unix(exp, 0)
}

// Kind 标识直链指向文档还是任务 zip。
type Kind string

const (
	KindDocument Kind = "d"
	KindJob      Kind = "j"
)

// Parsed 是校验后的直链内容。
type Parsed struct {
	Kind Kind
	ID   string
	Exp  time.Time
}

// Verify 校验 token：签名常量时间比较、未过期。
func (s *Signer) Verify(token string) (*Parsed, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed token")
	}
	payloadB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("bad payload encoding")
	}
	macB, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("bad sig encoding")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payloadB)
	expected := mac.Sum(nil)
	if !hmac.Equal(macB, expected) {
		return nil, fmt.Errorf("signature mismatch")
	}
	fields := strings.Split(string(payloadB), ":")
	if len(fields) != 3 {
		return nil, fmt.Errorf("malformed payload")
	}
	kind, id, expStr := fields[0], fields[1], fields[2]
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bad exp")
	}
	if kind != "d" && kind != "j" {
		return nil, fmt.Errorf("unknown kind")
	}
	p := &Parsed{Kind: Kind(kind), ID: id, Exp: time.Unix(expUnix, 0)}
	if time.Now().After(p.Exp) {
		return nil, fmt.Errorf("token expired")
	}
	return p, nil
}

// TTL 返回配置的有效期。
func (s *Signer) TTL() time.Duration { return s.ttl }
