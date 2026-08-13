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

// Allow 对 key 在 window 内计数,超过 limit 返回 false。
func (l *Limiter) Allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || now.Sub(e.windowStart) >= window {
		l.entries[key] = &entry{windowStart: now, count: 1}
		return true
	}
	if e.count >= limit {
		return false
	}
	e.count++
	return true
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
