package filesystem

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type testFactory struct {
	code  filesystemCode
	label string
	disk  *testDisk
	err   error
}

type filesystemCode = Code

func (f testFactory) Code() Code    { return Code(f.code) }
func (f testFactory) Label() string { return f.label }
func (f testFactory) Open(context.Context) (Disk, error) {
	return f.disk, f.err
}

type testDisk struct {
	code       Code
	visibility Visibility
	pings      atomic.Int32
	closes     atomic.Int32
}

func (d *testDisk) Code() Code                 { return d.code }
func (d *testDisk) Visibility() Visibility     { return d.visibility }
func (d *testDisk) Ping(context.Context) error { d.pings.Add(1); return nil }
func (d *testDisk) Close() error               { d.closes.Add(1); return nil }
func (d *testDisk) PutNew(context.Context, string, io.Reader, string) error {
	return nil
}
func (d *testDisk) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrNotFound
}
func (d *testDisk) Delete(context.Context, string) error { return nil }
func (d *testDisk) URL(context.Context, Reference) (string, error) {
	return "", nil
}
func (d *testDisk) TemporaryURL(
	context.Context,
	Reference,
	time.Time,
) (string, error) {
	return "", nil
}

func TestManagerOpensResolvesAndClosesDisks(t *testing.T) {
	public := &testDisk{code: "public", visibility: VisibilityPublic}
	private := &testDisk{code: "private", visibility: VisibilityPrivate}

	manager, err := NewManager(context.Background(), []Factory{
		testFactory{code: "public", label: "Public files", disk: public},
		testFactory{code: "private", label: "Private files", disk: private},
	})
	if err != nil {
		t.Fatal(err)
	}
	if disk, exists := manager.Disk("private"); !exists || disk != private {
		t.Fatalf("private disk = %#v, %t", disk, exists)
	}
	infos := manager.Disks()
	if len(infos) != 2 || infos[0].Label != "Public files" || infos[1].Label != "Private files" {
		t.Fatalf("disk infos = %#v", infos)
	}
	if public.pings.Load() != 1 || private.pings.Load() != 1 {
		t.Fatalf("ping counts = %d, %d", public.pings.Load(), private.pings.Load())
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if public.closes.Load() != 1 || private.closes.Load() != 1 {
		t.Fatalf("close counts = %d, %d", public.closes.Load(), private.closes.Load())
	}
}

func TestManagerFallsBackToCodeWhenFactoryLabelIsEmpty(t *testing.T) {
	manager, err := NewManager(context.Background(), []Factory{
		testFactory{
			code: "archive",
			disk: &testDisk{code: "archive", visibility: VisibilityPrivate},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	infos := manager.Disks()
	if len(infos) != 1 || infos[0].Label != "archive" {
		t.Fatalf("disk infos = %#v", infos)
	}
}

func TestManagerRejectsDuplicatesAndClosesPartialOpen(t *testing.T) {
	opened := &testDisk{code: "public", visibility: VisibilityPublic}
	_, err := NewManager(context.Background(), []Factory{
		testFactory{code: "public", disk: opened},
		testFactory{code: "broken", err: errors.New("open failed")},
	})
	if err == nil {
		t.Fatal("expected open error")
	}
	if opened.closes.Load() != 1 {
		t.Fatalf("partial disk close count = %d", opened.closes.Load())
	}

	_, err = NewManager(context.Background(), []Factory{
		testFactory{
			code: "same",
			disk: &testDisk{code: "same", visibility: VisibilityPublic},
		},
		testFactory{
			code: "same",
			disk: &testDisk{code: "same", visibility: VisibilityPrivate},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate disk error")
	}
}
