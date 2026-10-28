package auth

import (
	"sync"
	"time"
)

// Limiter 是单实例内存令牌桶：每个 API Key 按 qps_limit 放行。
type Limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens   float64
	last     time.Time
	perSec   int
}

// NewLimiter 构造。
func NewLimiter() *Limiter { return &Limiter{m: map[string]*bucket{}} }

// Allow 消耗一个令牌；qps<=0 表示不限制。
func (l *Limiter) Allow(keyID string, qps int) bool {
	if qps <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.m[keyID]
	if !ok {
		b = &bucket{tokens: float64(qps), last: now, perSec: qps}
		l.m[keyID] = b
	}
	if b.perSec != qps {
		b.perSec = qps
		b.tokens = float64(qps)
	}
	// 按流逝时间补令牌，上限 = qps。
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * float64(b.perSec)
	if b.tokens > float64(b.perSec) {
		b.tokens = float64(b.perSec)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
