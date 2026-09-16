package argon2id

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"golang.org/x/crypto/argon2"
)

const (
	defaultMemory  uint32 = 19 * 1024
	defaultTime    uint32 = 2
	defaultThreads uint8  = 1
	defaultSaltLen        = 16
	defaultKeyLen  uint32 = 32
)

type Config struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen int
	KeyLen  uint32
}

type Hasher struct {
	config Config
	random io.Reader
	dummy  string
}

type Factory struct {
	Config Config
}

func (f Factory) Open() (user.PasswordHasher, error) {
	return NewWithRandom(f.Config, rand.Reader)
}

func New() (*Hasher, error) {
	return NewWithRandom(Config{}, rand.Reader)
}

func NewWithRandom(
	config Config,
	random io.Reader,
) (*Hasher, error) {
	config = normalizeConfig(config)
	if random == nil {
		return nil, errors.New("argon2id random reader is nil")
	}
	hasher := &Hasher{config: config, random: random}
	dummySalt := make([]byte, config.SaltLen)
	for index := range dummySalt {
		dummySalt[index] = byte(index + 1)
	}
	hasher.dummy = encode(
		config,
		dummySalt,
		argon2.IDKey(
			[]byte("dummy-password-value"),
			dummySalt,
			config.Time,
			config.Memory,
			config.Threads,
			config.KeyLen,
		),
	)
	return hasher, nil
}

func (h *Hasher) Hash(password string) (string, error) {
	if h == nil {
		return "", errors.New("argon2id hasher is nil")
	}
	salt := make([]byte, h.config.SaltLen)
	if _, err := io.ReadFull(h.random, salt); err != nil {
		return "", fmt.Errorf("generate argon2id salt: %w", err)
	}
	hash := argon2.IDKey(
		[]byte(password),
		salt,
		h.config.Time,
		h.config.Memory,
		h.config.Threads,
		h.config.KeyLen,
	)
	return encode(h.config, salt, hash), nil
}

func (h *Hasher) Verify(
	password string,
	encoded string,
) (bool, bool, error) {
	if h == nil {
		return false, false, errors.New("argon2id hasher is nil")
	}
	config, salt, expected, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	actual := argon2.IDKey(
		[]byte(password),
		salt,
		config.Time,
		config.Memory,
		config.Threads,
		uint32(len(expected)),
	)
	valid := subtle.ConstantTimeCompare(actual, expected) == 1
	return valid, config != h.config, nil
}

func (h *Hasher) DummyHash() string {
	if h == nil {
		return ""
	}
	return h.dummy
}

func normalizeConfig(config Config) Config {
	if config.Memory == 0 {
		config.Memory = defaultMemory
	}
	if config.Time == 0 {
		config.Time = defaultTime
	}
	if config.Threads == 0 {
		config.Threads = defaultThreads
	}
	if config.SaltLen == 0 {
		config.SaltLen = defaultSaltLen
	}
	if config.KeyLen == 0 {
		config.KeyLen = defaultKeyLen
	}
	return config
}

var _ user.PasswordHasherFactory = Factory{}

func encode(config Config, salt, hash []byte) string {
	base64Encoding := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		config.Memory,
		config.Time,
		config.Threads,
		base64Encoding.EncodeToString(salt),
		base64Encoding.EncodeToString(hash),
	)
}

func decode(
	encoded string,
) (Config, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" ||
		parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Config{}, nil, nil, errors.New(
			"invalid argon2id hash encoding",
		)
	}

	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(
		parts[3],
		"m=%d,t=%d,p=%d",
		&memory,
		&iterations,
		&threads,
	); err != nil ||
		memory == 0 ||
		iterations == 0 ||
		threads == 0 ||
		parts[3] != fmt.Sprintf(
			"m=%d,t=%d,p=%d",
			memory,
			iterations,
			threads,
		) {
		return Config{}, nil, nil, errors.New(
			"invalid argon2id parameters",
		)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Config{}, nil, nil, errors.New(
			"invalid argon2id salt",
		)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) == 0 {
		return Config{}, nil, nil, errors.New(
			"invalid argon2id hash",
		)
	}

	return Config{
		Memory:  memory,
		Time:    iterations,
		Threads: threads,
		SaltLen: len(salt),
		KeyLen:  uint32(len(hash)),
	}, salt, hash, nil
}
