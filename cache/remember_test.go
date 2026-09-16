package cache

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRememberJSONMissLoadThenHit(t *testing.T) {
	store := &testStore{code: "memory", values: make(map[string][]byte)}
	var events []Event
	observed := observeStore(store, ObserverFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	}))
	type cachedValue struct {
		Count int `json:"count"`
	}
	calls := 0
	loader := func(context.Context) (cachedValue, error) {
		calls++
		return cachedValue{Count: 2}, nil
	}
	options := SetOptions{TTL: time.Minute, Tags: []Tag{"resource:7"}}

	first, err := RememberJSON(
		context.Background(), observed, "resource:7", options, loader,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RememberJSON(
		context.Background(), observed, "resource:7", options, loader,
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("calls = %d, first = %#v, second = %#v", calls, first, second)
	}
	if !containsEvent(events, EventMiss) || !containsEvent(events, EventHit) {
		t.Fatalf("events = %#v", events)
	}
}

func TestRememberJSONCorruptPayloadRecovers(t *testing.T) {
	store := &testStore{
		code: "memory",
		values: map[string][]byte{
			"resource:7": []byte("not-json"),
		},
	}
	var events []Event
	observed := observeStore(store, ObserverFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	}))

	result, err := RememberJSON(
		context.Background(),
		observed,
		"resource:7",
		SetOptions{},
		func(context.Context) (int, error) { return 42, nil },
	)
	if err != nil || result != 42 {
		t.Fatalf("result = %d, error = %v", result, err)
	}
	raw, err := store.Get(context.Background(), "resource:7")
	if err != nil {
		t.Fatal(err)
	}
	var cached int
	if err := json.Unmarshal(raw, &cached); err != nil || cached != 42 {
		t.Fatalf("cached = %d, error = %v", cached, err)
	}
	if !containsEvent(events, EventDecodeError) {
		t.Fatalf("events = %#v", events)
	}
}

func TestRememberJSONFailsOpenAndObservesBackendErrors(t *testing.T) {
	backendErr := errors.New("backend unavailable")
	store := &failingStore{code: "redis", readErr: backendErr, writeErr: backendErr}
	var events []Event
	observed := observeStore(store, ObserverFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	}))

	result, err := RememberJSON(
		context.Background(),
		observed,
		"resource:7",
		SetOptions{},
		func(context.Context) (string, error) { return "authoritative", nil },
	)
	if err != nil || result != "authoritative" {
		t.Fatalf("result = %q, error = %v", result, err)
	}
	if !containsEvent(events, EventReadError) ||
		!containsEvent(events, EventWriteError) {
		t.Fatalf("events = %#v", events)
	}
}

func TestRememberJSONDoesNotCacheLoaderError(t *testing.T) {
	store := &testStore{code: "memory", values: make(map[string][]byte)}
	loaderErr := errors.New("database unavailable")
	_, err := RememberJSON(
		context.Background(),
		store,
		"resource:7",
		SetOptions{},
		func(context.Context) (int, error) { return 0, loaderErr },
	)
	if !errors.Is(err, loaderErr) {
		t.Fatalf("error = %v", err)
	}
	if _, exists := store.values["resource:7"]; exists {
		t.Fatal("failed loader result was cached")
	}
}

func TestInvalidationFailureIsObserved(t *testing.T) {
	backendErr := errors.New("backend unavailable")
	store := &failingStore{code: "redis", invalidateErr: backendErr}
	var events []Event
	observed := observeStore(store, ObserverFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	}))
	if err := observed.InvalidateTag(context.Background(), "resource:7"); !errors.Is(err, backendErr) {
		t.Fatalf("error = %v", err)
	}
	if !containsEvent(events, EventInvalidationError) {
		t.Fatalf("events = %#v", events)
	}
}

func containsEvent(events []Event, eventType EventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type failingStore struct {
	code          Code
	readErr       error
	writeErr      error
	invalidateErr error
}

func (s *failingStore) Code() Code               { return s.code }
func (*failingStore) Ping(context.Context) error { return nil }
func (s *failingStore) Get(context.Context, string) ([]byte, error) {
	return nil, s.readErr
}
func (s *failingStore) Set(context.Context, string, []byte, SetOptions) error {
	return s.writeErr
}
func (*failingStore) Exists(context.Context, string) (bool, error) { return false, nil }
func (*failingStore) Delete(context.Context, string) error         { return nil }
func (s *failingStore) InvalidateTag(context.Context, Tag) error {
	return s.invalidateErr
}
func (*failingStore) Close() error { return nil }

var _ Store = (*failingStore)(nil)
