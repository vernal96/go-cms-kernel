package filesystemcache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/support/cacheentry"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

type Layout string

const (
	LayoutAuto    Layout = "auto"
	LayoutFlat    Layout = "flat"
	LayoutSharded Layout = "sharded"
)

type Config struct {
	Code    cache.Code
	Disk    filesystem.Code
	Layout  Layout
	Prefix  string
	MaxSize int64
	Now     func() time.Time
	Random  io.Reader
}

type Factory struct {
	Config Config
}

func (f Factory) Code() cache.Code {
	return f.Config.Code
}

func (f Factory) Open(
	ctx context.Context,
	dependencies cache.Dependencies,
) (cache.Store, error) {
	if dependencies.Filesystems == nil {
		return nil, errors.New("filesystem cache resolver is nil")
	}
	disk, exists := dependencies.Filesystems.Disk(f.Config.Disk)
	if !exists {
		return nil, fmt.Errorf(
			"filesystem cache disk %q: %w",
			f.Config.Disk,
			filesystem.ErrDiskNotFound,
		)
	}
	return New(ctx, f.Config, disk)
}

type Connector struct {
	cache.LoadLocks
	code    cache.Code
	disk    filesystem.Disk
	writer  filesystem.OverwriteDisk
	layout  Layout
	prefix  string
	maxSize int64
	now     func() time.Time
	random  io.Reader
}

func New(
	ctx context.Context,
	config Config,
	disk filesystem.Disk,
) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("filesystem cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Code == "" {
		return nil, errors.New("filesystem cache code is empty")
	}
	if disk == nil {
		return nil, errors.New("filesystem cache disk is nil")
	}
	if config.Disk == "" {
		config.Disk = disk.Code()
	}
	if disk.Code() != config.Disk {
		return nil, fmt.Errorf(
			"filesystem cache configured disk %q resolved disk %q",
			config.Disk,
			disk.Code(),
		)
	}
	if disk.Visibility() != filesystem.VisibilityPrivate {
		return nil, errors.New(
			"filesystem cache requires a private filesystem disk",
		)
	}
	writer, ok := disk.(filesystem.OverwriteDisk)
	if !ok {
		return nil, errors.New(
			"filesystem cache disk does not support atomic overwrite",
		)
	}

	layout := config.Layout
	if layout == "" {
		layout = LayoutAuto
	}
	if layout == LayoutAuto {
		provider, ok := disk.(filesystem.KeyDistributionProvider)
		if !ok {
			return nil, errors.New(
				"filesystem cache disk does not describe key distribution",
			)
		}
		switch provider.KeyDistribution() {
		case filesystem.KeyDistributionHierarchical:
			layout = LayoutSharded
		case filesystem.KeyDistributionSelfManaged:
			layout = LayoutFlat
		default:
			return nil, fmt.Errorf(
				"filesystem cache disk has invalid key distribution %q",
				provider.KeyDistribution(),
			)
		}
	}
	if layout != LayoutFlat && layout != LayoutSharded {
		return nil, fmt.Errorf(
			"filesystem cache layout %q is invalid",
			layout,
		)
	}

	prefix := strings.Trim(strings.TrimSpace(config.Prefix), "/")
	if prefix == "" {
		prefix = "cache/" + string(config.Code)
	}
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	if config.MaxSize < 0 {
		return nil, errors.New("filesystem cache max size is invalid")
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
		code:    config.Code,
		disk:    disk,
		writer:  writer,
		layout:  layout,
		prefix:  prefix,
		maxSize: config.MaxSize,
		now:     now,
		random:  random,
	}, nil
}

func (c *Connector) Code() cache.Code {
	return c.code
}

func (c *Connector) Ping(ctx context.Context) error {
	return c.disk.Ping(ctx)
}

func (c *Connector) Get(
	ctx context.Context,
	key string,
) ([]byte, error) {
	if err := validateContextAndKey(ctx, key); err != nil {
		return nil, err
	}
	objectKey := c.entryKey(key)
	raw, err := c.read(ctx, objectKey)
	if err != nil {
		return nil, err
	}
	entry, valid, err := c.validEntry(ctx, raw)
	if err != nil {
		_ = c.disk.Delete(ctx, objectKey)
		return nil, err
	}
	if !valid {
		_ = c.disk.Delete(ctx, objectKey)
		return nil, cache.ErrMiss
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
	if c.maxSize > 0 && int64(len(value)) > c.maxSize {
		return fmt.Errorf(
			"filesystem cache value exceeds %d bytes",
			c.maxSize,
		)
	}
	set, err := c.Prepare(ctx, options.Tags)
	if err != nil {
		return err
	}
	return set(ctx, key, value, options.TTL)
}

func (c *Connector) Prepare(ctx context.Context, dependencies []cache.Tag) (cache.PreparedSet, error) {
	tags, err := c.tagTokens(ctx, dependencies)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, key string, value []byte, ttl time.Duration) error {
		if err := validateContextAndKey(ctx, key); err != nil {
			return err
		}
		if ttl < 0 {
			return cache.ErrInvalidTTL
		}
		if c.maxSize > 0 && int64(len(value)) > c.maxSize {
			return fmt.Errorf("filesystem cache value exceeds %d bytes", c.maxSize)
		}
		var expiresAt int64
		if ttl > 0 {
			expiresAt = c.now().Add(ttl).UnixNano()
		}
		raw, err := cacheentry.Encode(cacheentry.Entry{
			ExpiresAt: expiresAt,
			Tags:      tags,
			Value:     append([]byte(nil), value...),
		})
		if err != nil {
			return fmt.Errorf("encode filesystem cache entry: %w", err)
		}
		if err := c.writer.Put(
			ctx,
			c.entryKey(key),
			bytes.NewReader(raw),
			"application/octet-stream",
		); err != nil {
			return fmt.Errorf("write filesystem cache entry: %w", err)
		}
		return nil
	}, nil
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
	return c.disk.Delete(ctx, c.entryKey(key))
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
		return fmt.Errorf("generate filesystem cache tag token: %w", err)
	}
	if err := c.writer.Put(
		ctx,
		c.tagKey(tag),
		bytes.NewReader(token[:]),
		"application/octet-stream",
	); err != nil {
		return fmt.Errorf("write filesystem cache tag token: %w", err)
	}
	return nil
}

// The filesystem manager owns the disk lifecycle.
func (*Connector) Close() error {
	return nil
}

func (c *Connector) Prune(ctx context.Context) error {
	walker, ok := c.disk.(filesystem.PrefixWalker)
	if !ok {
		return filesystem.ErrUnsupported
	}
	return walker.WalkPrefix(ctx, path.Join(c.prefix, "entries"), func(key string) error {
		raw, err := c.read(ctx, key)
		if errors.Is(err, cache.ErrMiss) {
			return nil
		}
		if err != nil {
			return err
		}
		_, valid, err := c.validEntry(ctx, raw)
		if errors.Is(err, cache.ErrCorrupt) {
			return c.disk.Delete(ctx, key)
		}
		if err != nil {
			return err
		}
		if valid {
			return nil
		}
		return c.disk.Delete(ctx, key)
	})
}

func (c *Connector) Flush(ctx context.Context) error {
	walker, ok := c.disk.(filesystem.PrefixWalker)
	if !ok {
		return filesystem.ErrUnsupported
	}
	var keys []string
	if err := walker.WalkPrefix(ctx, c.prefix, func(key string) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		return err
	}
	var result []error
	for _, key := range keys {
		if err := c.disk.Delete(ctx, key); err != nil {
			result = append(result, err)
		}
	}
	return errors.Join(result...)
}

func (c *Connector) validEntry(
	ctx context.Context,
	raw []byte,
) (cacheentry.Entry, bool, error) {
	entry, err := cacheentry.Decode(raw)
	if err != nil {
		return cacheentry.Entry{}, false, fmt.Errorf(
			"%w: %w: %v",
			cache.ErrMiss,
			cache.ErrCorrupt,
			err,
		)
	}
	if entry.ExpiresAt > 0 &&
		!c.now().Before(time.Unix(0, entry.ExpiresAt)) {
		return cacheentry.Entry{}, false, nil
	}
	for tag, expected := range entry.Tags {
		current, err := c.tagToken(ctx, cache.Tag(tag))
		if err != nil {
			return cacheentry.Entry{}, false, err
		}
		if current != expected {
			return cacheentry.Entry{}, false, nil
		}
	}
	return entry, true, nil
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
	raw, err := c.read(ctx, c.tagKey(tag))
	if errors.Is(err, cache.ErrMiss) {
		return cacheentry.Token{}, nil
	}
	if err != nil {
		return cacheentry.Token{}, err
	}
	if len(raw) != len(cacheentry.Token{}) {
		if err := c.InvalidateTag(ctx, tag); err != nil {
			return cacheentry.Token{}, errors.Join(
				errors.New("filesystem cache tag token is corrupt"),
				err,
			)
		}
		raw, err = c.read(ctx, c.tagKey(tag))
		if err != nil {
			return cacheentry.Token{}, err
		}
	}
	var result cacheentry.Token
	copy(result[:], raw)
	return result, nil
}

func (c *Connector) read(
	ctx context.Context,
	key string,
) ([]byte, error) {
	reader, err := c.disk.Open(ctx, key)
	if errors.Is(err, filesystem.ErrNotFound) {
		return nil, cache.ErrMiss
	}
	if err != nil {
		return nil, fmt.Errorf("open filesystem cache object: %w", err)
	}
	raw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return raw, nil
}

func (c *Connector) entryKey(key string) string {
	return c.objectKey("entries", key)
}

func (c *Connector) tagKey(tag cache.Tag) string {
	return c.objectKey("tags", string(tag))
}

func (c *Connector) objectKey(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	if c.layout == LayoutSharded {
		return path.Join(c.prefix, kind, hash[:2], hash[2:4], hash)
	}
	return path.Join(c.prefix, kind, hash)
}

func validateContextAndKey(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("filesystem cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return errors.New("filesystem cache key is empty")
	}
	return nil
}

func validateContextAndTag(ctx context.Context, tag cache.Tag) error {
	if ctx == nil {
		return errors.New("filesystem cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tag == "" {
		return errors.New("filesystem cache tag is empty")
	}
	return nil
}

func validatePrefix(prefix string) error {
	for _, part := range strings.Split(prefix, "/") {
		if part == "" || part == "." || part == ".." ||
			strings.ContainsRune(part, '\x00') {
			return errors.New("filesystem cache prefix is invalid")
		}
	}
	return nil
}

var _ cache.Factory = Factory{}
var _ cache.Store = (*Connector)(nil)
var _ cache.Pruner = (*Connector)(nil)
var _ cache.Flusher = (*Connector)(nil)
