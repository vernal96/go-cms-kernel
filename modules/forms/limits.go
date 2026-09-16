package forms

import (
	"errors"
	"sync"
	"time"
)

type PublicLimits struct {
	MaxRequestSize      int64
	MaxScalarFields     int
	MaxScalarValueSize  int64
	MaxUploadFileSize   int64
	MaxUploadCount      int
	MaxTotalUploadBytes int64
	SubmissionTimeout   time.Duration
	RateLimit           int
	RateWindow          time.Duration
	RateEntries         int
	StoreClientAddress  bool
}

func (l PublicLimits) Validate() error {
	if l.MaxRequestSize < 1 || l.MaxScalarFields < 1 || l.MaxScalarValueSize < 1 ||
		l.MaxUploadFileSize < 1 || l.MaxUploadCount < 1 || l.MaxTotalUploadBytes < 1 ||
		l.MaxUploadFileSize > l.MaxTotalUploadBytes || l.SubmissionTimeout <= 0 ||
		l.RateLimit < 1 || l.RateWindow <= 0 || l.RateEntries < 1 {
		return errors.New("Forms public limits are invalid")
	}
	return nil
}

type rateEntry struct {
	window  time.Time
	count   int
	touched time.Time
}

type submitRateLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	entries    map[string]rateEntry
}

func newSubmitRateLimiter(limit int, window time.Duration, maxEntries int) *submitRateLimiter {
	return &submitRateLimiter{limit: limit, window: window, maxEntries: maxEntries, entries: make(map[string]rateEntry)}
}

func (l *submitRateLimiter) Allow(key string, now time.Time) bool {
	if l == nil || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, exists := l.entries[key]
	if !exists || now.Sub(entry.window) >= l.window {
		if !exists && len(l.entries) >= l.maxEntries {
			l.evict(now)
		}
		l.entries[key] = rateEntry{window: now, count: 1, touched: now}
		return true
	}
	entry.touched = now
	if entry.count >= l.limit {
		l.entries[key] = entry
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

func (l *submitRateLimiter) evict(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.window) >= l.window {
			delete(l.entries, key)
		}
	}
	for len(l.entries) >= l.maxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range l.entries {
			if oldestKey == "" || entry.touched.Before(oldest) {
				oldestKey, oldest = key, entry.touched
			}
		}
		if oldestKey == "" {
			break
		}
		delete(l.entries, oldestKey)
	}
}
