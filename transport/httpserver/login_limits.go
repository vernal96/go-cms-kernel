package httpserver

import (
	"crypto/sha256"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginWindow struct {
	count int
	until time.Time
}
type loginLimits struct {
	mu        sync.Mutex
	windows   map[[32]byte]loginWindow
	nextPrune time.Time
	slots     chan struct{}
}

func newLoginLimits() *loginLimits {
	return &loginLimits{windows: make(map[[32]byte]loginWindow), slots: make(chan struct{}, 2)}
}
func (l *loginLimits) allow(request *http.Request, identifier string) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.nextPrune) {
		for k, w := range l.windows {
			if !now.Before(w.until) {
				delete(l.windows, k)
			}
		}
		l.nextPrune = now.Add(time.Minute)
	}
	for index, key := range []string{"ip:" + host, "login:" + host + ":" + strings.ToLower(strings.TrimSpace(identifier))} {
		hash := sha256.Sum256([]byte(key))
		w, exists := l.windows[hash]
		if !exists && len(l.windows) >= 10000 {
			return false
		}
		if !now.Before(w.until) {
			w = loginWindow{until: now.Add(time.Minute)}
		}
		limit := 30
		if index == 1 {
			limit = 10
		}
		if w.count >= limit {
			return false
		}
		w.count++
		l.windows[hash] = w
	}
	return true
}
