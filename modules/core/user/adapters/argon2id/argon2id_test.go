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

func TestRejectsUnsafeConfigurationBeforeHashing(t *testing.T) {
	for name, config := range map[string]Config{
		"negative salt":     {SaltLen: -1},
		"huge salt":         {SaltLen: 1 << 30},
		"short salt":        {SaltLen: 1},
		"huge key":          {KeyLen: ^uint32(0)},
		"short key":         {KeyLen: 1},
		"huge memory":       {Memory: ^uint32(0)},
		"memory per thread": {Memory: 8, Threads: 2},
		"huge iterations":   {Time: ^uint32(0)},
		"huge threads":      {Threads: 255},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWithRandom(config, bytes.NewReader(nil)); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	var zero Hasher
	if _, err := zero.Hash("password"); err == nil {
		t.Fatal("zero-value hasher accepted")
	}
}

func TestVerifyRejectsUnboundedStoredParameters(t *testing.T) {
	hasher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string]string{
		"memory":     "$argon2id$v=19$m=4294967295,t=2,p=1$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"iterations": "$argon2id$v=19$m=8192,t=4294967295,p=1$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"threads":    "$argon2id$v=19$m=8192,t=2,p=255$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"length":     strings.Repeat("x", maximumEncodedLen+1),
		"short salt": "$argon2id$v=19$m=8192,t=2,p=1$YQ$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"short key":  "$argon2id$v=19$m=8192,t=2,p=1$MTIzNDU2Nzg$YQ",
	} {
		t.Run(name, func(t *testing.T) {
			valid, _, err := hasher.Verify("password", encoded)
			if err == nil || valid {
				t.Fatalf("unsafe PHC string accepted: valid=%v err=%v", valid, err)
			}
		})
	}
}
