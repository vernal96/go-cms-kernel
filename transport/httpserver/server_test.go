package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownTimeoutCancelsActiveRequests(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer backend.Close()
	defer backend.CloseClientConnections()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := backend.Client().Get(backend.URL)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	server := &Server{server: backend.Config, config: Config{ShutdownTimeout: 10 * time.Millisecond}}
	if err := server.shutdown(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active request context was not canceled after shutdown timeout")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client connection remained open after shutdown timeout")
	}
}
