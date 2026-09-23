package httpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

type blockingLogin struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (a *blockingLogin) Authenticate(context.Context, user.AuthenticateInput) (user.User, error) {
	a.calls.Add(1)
	a.started <- struct{}{}
	<-a.release
	return user.User{}, user.ErrInvalidCredentials
}
func TestLoginConcurrentWorkHasHardLimit(t *testing.T) {
	auth := &blockingLogin{started: make(chan struct{}, 2), release: make(chan struct{})}
	h, _ := newLoginHandler(auth, &stubAccessTokens{})
	call := func() int {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"identifier":"user","password":"wrong"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	done := make(chan int, 2)
	for range 2 {
		go func() { done <- call() }()
	}
	for range 2 {
		<-auth.started
	}
	if status := call(); status != 503 {
		t.Errorf("capacity status %d", status)
	}
	close(auth.release)
	for range 2 {
		<-done
	}
	if auth.calls.Load() != 2 {
		t.Fatal("exceeded hashing capacity")
	}
}
func TestLoginRepeatedFailuresAreThrottled(t *testing.T) {
	h, _ := newLoginHandler(&stubAuthenticator{err: user.ErrInvalidCredentials}, &stubAccessTokens{})
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"identifier":"user","password":"wrong"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 401
		if i == 10 {
			expected = 429
		}
		if w.Code != expected {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
}
