// Package auth — ratelock.go：登录失败锁定与认证类接口 IP 限流（内存态）。
package auth

import (
	"sort"
	"sync"
	"time"
)

const (
	maxFails      = 5
	lockDuration  = 5 * time.Minute
	ipLimitPerMin = 20

	// guardMaxEntries 是两张内存表各自的条目上限。
	//
	// 这两张表按「提交上来的用户名」和「来源 IP」建键，而这两者都是外部可控的：
	// 想打满进程内存，只需要拿随机用户名连登录接口（每个名字一条记录，永不回收）。
	// 一次请求一个 map 条目看起来很小，但这是公网接口，而且是「不用登录就能打」的那种。
	// 超过上限就做一次清扫，把已经不可能再影响判断的记录删掉。
	guardMaxEntries = 20000
)

type failRecord struct {
	count       int
	lockedUntil time.Time
	// lastSeen 用于清扫：锁定中的记录一律保留，其余按「多久没再出现」回收。
	lastSeen time.Time
}

type ipRecord struct {
	counts []time.Time
}

// LoginGuard 记录账号失败次数与 IP 请求频次。
type LoginGuard struct {
	mu    sync.Mutex
	fails map[string]*failRecord
	ips   map[string]*ipRecord
}

// NewLoginGuard 构造。
func NewLoginGuard() *LoginGuard {
	return &LoginGuard{fails: map[string]*failRecord{}, ips: map[string]*ipRecord{}}
}

// LockedRemaining 返回账号当前锁定剩余秒数；未锁定返回 0。
func (g *LoginGuard) LockedRemaining(username string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.fails[username]
	if r == nil {
		return 0
	}
	if d := time.Until(r.lockedUntil); d > 0 {
		return int(d.Seconds())
	}
	return 0
}

// RecordFailure 记录一次失败；达到阈值进入锁定。
func (g *LoginGuard) RecordFailure(username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if len(g.fails) >= guardMaxEntries {
		g.sweepFailsLocked(now)
	}
	r := g.fails[username]
	if r == nil {
		r = &failRecord{}
		g.fails[username] = r
	}
	r.lastSeen = now
	r.count++
	if r.count >= maxFails {
		r.lockedUntil = now.Add(lockDuration)
		r.count = 0
	}
}

// Clear 登录成功后清除失败计数。
func (g *LoginGuard) Clear(username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, username)
}

// AllowIP 判断该 IP 在认证类接口上是否还有额度（滑动窗口 1 分钟，上限 20）。
func (g *LoginGuard) AllowIP(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	if len(g.ips) >= guardMaxEntries {
		g.sweepIPsLocked(cut)
	}
	r := g.ips[ip]
	if r == nil {
		r = &ipRecord{}
		g.ips[ip] = r
	}
	kept := r.counts[:0]
	for _, t := range r.counts {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= ipLimitPerMin {
		r.counts = kept
		return false
	}
	r.counts = append(kept, now)
	return true
}

// sweepFailsLocked 在表满时腾出空间。
//
// 分两步：
//  1. 先按时间回收：锁定期早已过去、且两倍锁定期内没再出现的记录，回收它没有风险；
//  2. 这一步通常是空的——因为被刷的时候每条记录都是"刚刚写过"的，按时间一条也删不掉。
//     所以还要有硬上限：按 lastSeen 淘汰最旧的那批到 75% 容量。
//
// 两步都绝不碰「仍在锁定中」的记录：删掉那条等于给正在爆破的人当场解锁，
// 而这恰恰是攻击者最希望发生的事。极端情况下（锁定记录本身就超过上限）
// 表会超过上限——宁可多占内存，也不放开锁定。
//
// 调用方必须已持有 g.mu。
func (g *LoginGuard) sweepFailsLocked(now time.Time) {
	cut := now.Add(-2 * lockDuration)
	for k, r := range g.fails {
		if now.Before(r.lockedUntil) {
			continue
		}
		if r.lastSeen.Before(cut) {
			delete(g.fails, k)
		}
	}
	if len(g.fails) <= guardMaxEntries {
		return
	}
	want := guardMaxEntries * 3 / 4
	type entry struct {
		key  string
		seen time.Time
	}
	cand := make([]entry, 0, len(g.fails))
	for k, r := range g.fails {
		if now.Before(r.lockedUntil) {
			continue
		}
		cand = append(cand, entry{k, r.lastSeen})
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].seen.Before(cand[j].seen) })
	for i := 0; i < len(cand) && len(g.fails) > want; i++ {
		delete(g.fails, cand[i].key)
	}
}

// sweepIPsLocked 回收「1 分钟内没有任何请求」的 IP 记录——这类记录已经不可能
// 再影响限流判断。仍然超额时按同样的理由做硬淘汰：被清掉的那条会从零重新计数，
// 代价是那个 IP 多一次尝试机会；而不清就是把内存交给外部输入决定。
// 调用方必须已持有 g.mu。
func (g *LoginGuard) sweepIPsLocked(cut time.Time) {
	for k, r := range g.ips {
		if len(r.counts) == 0 || !r.counts[len(r.counts)-1].After(cut) {
			delete(g.ips, k)
		}
	}
	want := guardMaxEntries * 3 / 4
	for k := range g.ips {
		if len(g.ips) <= want {
			break
		}
		delete(g.ips, k)
	}
}
