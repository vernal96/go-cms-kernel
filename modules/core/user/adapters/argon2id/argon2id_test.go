package argon2id

import (
	"bytes"
	"strings"
	"testing"
)

func TestHashVerifyAndRehashProfile(t *testing.T) {
	t.Parallel()

	salt := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
	}
	hasher, err := NewWithRandom(Config{}, bytes.NewReader(salt))
	if err != nil {
		t.Fatalf("new hasher: %v", err)
	}
	encoded, err := hasher.Hash("admin-dev-only-2026")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	t.Logf("dev seed hash: %s", encoded)

	valid, needsRehash, err := hasher.Verify(
		"admin-dev-only-2026",
		encoded,
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !valid || needsRehash {
		t.Fatalf(
			"unexpected verification result valid=%v rehash=%v",
			valid,
			needsRehash,
		)
	}
	valid, _, err = hasher.Verify("wrong-password", encoded)
	if err != nil {
		t.Fatalf("verify wrong password: %v", err)
	}
	if valid {
		t.Fatal("wrong password verified")
	}
}

func TestVerifyDetectsOldProfileAndMalformedEncoding(t *testing.T) {
	t.Parallel()

	old, err := NewWithRandom(
		Config{Memory: 8 * 1024},
		bytes.NewReader(bytes.Repeat([]byte{7}, 16)),
	)
	if err != nil {
		t.Fatalf("new old hasher: %v", err)
	}
	encoded, err := old.Hash("a-valid-password")
	if err != nil {
		t.Fatalf("hash old profile: %v", err)
	}
	current, err := NewWithRandom(
		Config{},
		bytes.NewReader(bytes.Repeat([]byte{8}, 16)),
	)
	if err != nil {
		t.Fatalf("new current hasher: %v", err)
	}
	valid, needsRehash, err := current.Verify(
		"a-valid-password",
		encoded,
	)
	if err != nil {
		t.Fatalf("verify old profile: %v", err)
	}
	if !valid || !needsRehash {
		t.Fatalf(
			"unexpected old profile result valid=%v rehash=%v",
			valid,
			needsRehash,
		)
	}
	if _, _, err := current.Verify(
		"a-valid-password",
		"not-a-phc-string",
	); err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("expected encoding error, got %v", err)
	}
}
