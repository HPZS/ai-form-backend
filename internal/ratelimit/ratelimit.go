// ai-form-backend - AGPL-3.0
// 单实例内存限流:固定窗口计数。阈值由调用方传入(生产阈值放私有配置,不硬编码进公开仓库)。
package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	windowStart time.Time
	count       int
}

type Limiter struct {
	mu      sync.Mutex
	entries map[string]*entry
}

func New() *Limiter {
	l := &Limiter{entries: map[string]*entry{}}
	go l.gc()
	return l
}

// Rule 一条限流规则。
type Rule struct {
	Key    string
	Limit  int
	Window time.Duration
}

// Allow 对 key 在 window 内计数,超过 limit 返回 false。
func (l *Limiter) Allow(key string, limit int, window time.Duration) bool {
	return l.AllowAll(Rule{Key: key, Limit: limit, Window: window}) == ""
}

// AllowAll 多条规则整体判定:全部有余量才一起计数;任一条超限则一条都不计,
// 返回超限的 key(全部放行时返回空串)。
//
// 逐条 Allow 串 `||` 会让"后一条拒绝"的请求白白吃掉前几条的配额:请求没被服务,
// 却在分钟窗口里占了名额,用户可用次数被凭空削减,且拒绝原因也无法定位到具体规则。
func (l *Limiter) AllowAll(rules ...Rule) string {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range rules {
		e := l.entries[r.Key]
		if e != nil && now.Sub(e.windowStart) < r.Window && e.count >= r.Limit {
			return r.Key
		}
	}
	for _, r := range rules {
		e := l.entries[r.Key]
		if e == nil || now.Sub(e.windowStart) >= r.Window {
			l.entries[r.Key] = &entry{windowStart: now, count: 1}
			continue
		}
		e.count++
	}
	return ""
}

func (l *Limiter) gc() {
	for range time.Tick(10 * time.Minute) {
		cutoff := time.Now().Add(-25 * time.Hour)
		l.mu.Lock()
		for k, e := range l.entries {
			if e.windowStart.Before(cutoff) {
				delete(l.entries, k)
			}
		}
		l.mu.Unlock()
	}
}
