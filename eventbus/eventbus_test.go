package eventbus_test

import (
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/eventbus"
)

func TestErrClosedIsStableSentinel(t *testing.T) {
	if !errors.Is(eventbus.ErrClosed, eventbus.ErrClosed) {
		t.Fatal("eventbus.ErrClosed is not a stable sentinel")
	}
}
