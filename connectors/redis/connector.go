package redis

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/vernal96/go-cms-kernel/connectors/support/cacheentry"
	"github.com/vernal96/go-cms-kernel/cache"
)

type Config struct {
	Code             cache.Code
	Addrs            []string
	ClientName       string
	Username         string
	Password         string
	DB               int
	MasterName       string
	SentinelUsername string
	SentinelPassword string
	Protocol         int
	MaxRetries       int
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	PoolTimeout      time.Duration
	PoolSize         int
	MinIdleConns     int
	MaxIdleConns     int
	ConnMaxIdleTime  time.Duration
	ConnMaxLifetime  time.Duration
	TLSEnabled       bool
	TLSServerName    string
	TLSInsecure      bool
	Prefix           string
	Now              func() time.Time
	Random           io.Reader
}

type Factory struct {
	Config Config
}

func (f Factory) Code() cache.Code {
	return f.Config.Code
}

func (f Factory) Open(
	ctx context.Context,
	_ cache.Dependencies,
) (cache.Store, error) {
	return New(ctx, f.Config)
}

type client interface {
	Ping(context.Context) error
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
	Close() error
}

type universalClient struct {
	client goredis.UniversalClient
}

func (c universalClient) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c universalClient) Get(
	ctx context.Context,
	key string,
) ([]byte, error) {
	result, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, cache.ErrMiss
	}
	return result, err
}

func (c universalClient) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c universalClient) Delete(
	ctx context.Context,
	key string,
) error {
	return c.client.Del(ctx, key).Err()
}

func (c universalClient) Close() error {
	return c.client.Close()
}

type Connector struct {
	code   cache.Code
	client client
	prefix string
	now    func() time.Time
	random io.Reader
}

func New(
	ctx context.Context,
	config Config,
) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("redis cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := universalOptions(config)
	if err != nil {
		return nil, err
	}
	if strings.ContainsRune(config.Prefix, '\x00') {
		return nil, errors.New("redis cache prefix contains NUL")
	}
	if strings.TrimSpace(config.Prefix) != "" &&
		strings.Trim(strings.TrimSpace(config.Prefix), ":") == "" {
		return nil, errors.New("redis cache prefix is invalid")
	}
	return newConnector(
		config,
		universalClient{client: goredis.NewUniversalClient(options)},
	), nil
}

func newConnector(config Config, backend client) *Connector {
	prefix := strings.TrimSpace(config.Prefix)
	if prefix == "" {
		prefix = "cms:cache:" + string(config.Code)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	return &Connector{
		code:   config.Code,
		client: backend,
		prefix: strings.TrimRight(prefix, ":"),
		now:    now,
		random: random,
	}
}

func universalOptions(config Config) (*goredis.UniversalOptions, error) {
	if config.Code == "" {
		return nil, errors.New("redis cache code is empty")
	}
	addrs := make([]string, 0, len(config.Addrs))
	for _, address := range config.Addrs {
		if address = strings.TrimSpace(address); address != "" {
			addrs = append(addrs, address)
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("redis cache addresses are empty")
	}
	if config.DB < 0 {
		return nil, errors.New("redis cache DB is invalid")
	}
	if config.Protocol != 0 &&
		config.Protocol != 2 &&
		config.Protocol != 3 {
		return nil, errors.New("redis cache protocol is invalid")
	}

	options := &goredis.UniversalOptions{
		Addrs:            addrs,
		ClientName:       config.ClientName,
		Username:         config.Username,
		Password:         config.Password,
		DB:               config.DB,
		MasterName:       config.MasterName,
		SentinelUsername: config.SentinelUsername,
		SentinelPassword: config.SentinelPassword,
		Protocol:         config.Protocol,
		MaxRetries:       config.MaxRetries,
		DialTimeout:      config.DialTimeout,
		ReadTimeout:      config.ReadTimeout,
		WriteTimeout:     config.WriteTimeout,
		PoolTimeout:      config.PoolTimeout,
		PoolSize:         config.PoolSize,
		MinIdleConns:     config.MinIdleConns,
		MaxIdleConns:     config.MaxIdleConns,
		ConnMaxIdleTime:  config.ConnMaxIdleTime,
		ConnMaxLifetime:  config.ConnMaxLifetime,
	}
	if config.TLSEnabled {
		options.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         config.TLSServerName,
			InsecureSkipVerify: config.TLSInsecure, //nolint:gosec
		}
	}
	return options, nil
}

func (c *Connector) Code() cache.Code {
	return c.code
}

func (c *Connector) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.New("redis cache ping context is nil")
	}
	return c.client.Ping(ctx)
}

func (c *Connector) Get(
	ctx context.Context,
	key string,
) ([]byte, error) {
	if err := validateContextAndKey(ctx, key); err != nil {
		return nil, err
	}
	physicalKey := c.entryKey(key)
	raw, err := c.client.Get(ctx, physicalKey)
	if err != nil {
		return nil, err
	}
	entry, err := cacheentry.Decode(raw)
	if err != nil {
		_ = c.client.Delete(ctx, physicalKey)
		return nil, fmt.Errorf(
			"%w: %w: %v",
			cache.ErrMiss,
			cache.ErrCorrupt,
			err,
		)
	}
	if entry.ExpiresAt > 0 &&
		!c.now().Before(time.Unix(0, entry.ExpiresAt)) {
		_ = c.client.Delete(ctx, physicalKey)
		return nil, cache.ErrMiss
	}
	for tag, expected := range entry.Tags {
		current, err := c.tagToken(ctx, cache.Tag(tag))
		if err != nil {
			return nil, err
		}
		if current != expected {
			_ = c.client.Delete(ctx, physicalKey)
			return nil, cache.ErrMiss
		}
	}
	return append([]byte(nil), entry.Value...), nil
}

func (c *Connector) Set(
	ctx context.Context,
	key string,
	value []byte,
	options cache.SetOptions,
) error {
	if err := validateContextAndKey(ctx, key); err != nil {
		return err
	}
	if options.TTL < 0 {
		return cache.ErrInvalidTTL
	}
	tags, err := c.tagTokens(ctx, options.Tags)
	if err != nil {
		return err
	}
	var expiresAt int64
	if options.TTL > 0 {
		expiresAt = c.now().Add(options.TTL).UnixNano()
	}
	raw, err := cacheentry.Encode(cacheentry.Entry{
		ExpiresAt: expiresAt,
		Tags:      tags,
		Value:     append([]byte(nil), value...),
	})
	if err != nil {
		return fmt.Errorf("encode redis cache entry: %w", err)
	}
	if err := c.client.Set(ctx, c.entryKey(key), raw, options.TTL); err != nil {
		return fmt.Errorf("write redis cache entry: %w", err)
	}
	return nil
}

func (c *Connector) Exists(
	ctx context.Context,
	key string,
) (bool, error) {
	_, err := c.Get(ctx, key)
	if errors.Is(err, cache.ErrMiss) {
		return false, nil
	}
	return err == nil, err
}

func (c *Connector) Delete(
	ctx context.Context,
	key string,
) error {
	if err := validateContextAndKey(ctx, key); err != nil {
		return err
	}
	return c.client.Delete(ctx, c.entryKey(key))
}

func (c *Connector) InvalidateTag(
	ctx context.Context,
	tag cache.Tag,
) error {
	if err := validateContextAndTag(ctx, tag); err != nil {
		return err
	}
	var token cacheentry.Token
	if _, err := io.ReadFull(c.random, token[:]); err != nil {
		return fmt.Errorf("generate redis cache tag token: %w", err)
	}
	if err := c.client.Set(ctx, c.tagKey(tag), token[:], 0); err != nil {
		return fmt.Errorf("write redis cache tag token: %w", err)
	}
	return nil
}

func (c *Connector) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Connector) tagTokens(
	ctx context.Context,
	tags []cache.Tag,
) (map[string]cacheentry.Token, error) {
	result := make(map[string]cacheentry.Token, len(tags))
	for _, tag := range tags {
		if err := validateContextAndTag(ctx, tag); err != nil {
			return nil, err
		}
		if _, exists := result[string(tag)]; exists {
			continue
		}
		token, err := c.tagToken(ctx, tag)
		if err != nil {
			return nil, err
		}
		result[string(tag)] = token
	}
	return result, nil
}

func (c *Connector) tagToken(
	ctx context.Context,
	tag cache.Tag,
) (cacheentry.Token, error) {
	raw, err := c.client.Get(ctx, c.tagKey(tag))
	if errors.Is(err, cache.ErrMiss) {
		return cacheentry.Token{}, nil
	}
	if err != nil {
		return cacheentry.Token{}, err
	}
	if len(raw) != len(cacheentry.Token{}) {
		if err := c.InvalidateTag(ctx, tag); err != nil {
			return cacheentry.Token{}, errors.Join(
				errors.New("redis cache tag token is corrupt"),
				err,
			)
		}
		raw, err = c.client.Get(ctx, c.tagKey(tag))
		if err != nil {
			return cacheentry.Token{}, err
		}
	}
	var result cacheentry.Token
	copy(result[:], raw)
	return result, nil
}

func (c *Connector) entryKey(key string) string {
	return c.prefix + ":entry:" + digest(key)
}

func (c *Connector) tagKey(tag cache.Tag) string {
	return c.prefix + ":tag:" + digest(string(tag))
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validateContextAndKey(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("redis cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return errors.New("redis cache key is empty")
	}
	return nil
}

func validateContextAndTag(ctx context.Context, tag cache.Tag) error {
	if ctx == nil {
		return errors.New("redis cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tag == "" {
		return errors.New("redis cache tag is empty")
	}
	return nil
}

var _ cache.Factory = Factory{}
var _ cache.Store = (*Connector)(nil)
