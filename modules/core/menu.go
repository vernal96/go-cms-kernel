package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type menuEnvelope struct {
	Result    resource.MenuResult `json:"result"`
	ExpiresAt time.Time           `json:"expires_at"`
}

// The embedded deadline also protects against cache TTL rounding and clock boundaries.
type menuDeadlineStore struct {
	cache.Store
	now    func() time.Time
	logger *slog.Logger
}

func (s menuDeadlineStore) LockLoad(ctx context.Context, key string) (func(), error) {
	if locks, ok := s.Store.(cache.LoadLocker); ok {
		return locks.LockLoad(ctx, key)
	}
	return func() {}, nil
}
func (s menuDeadlineStore) Prepare(ctx context.Context, tags []cache.Tag) (cache.PreparedSet, error) {
	return cache.Prepare(ctx, s.Store, tags), nil
}

func (s menuDeadlineStore) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := s.Store.Get(ctx, key)
	if err == nil {
		var envelope menuEnvelope
		decodeErr := json.Unmarshal(raw, &envelope)
		if decodeErr != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "invalid menu cache payload", "event", "cache.decode_error", "cache.key", key, "error", decodeErr)
		}
		if decodeErr == nil && !s.now().Before(envelope.ExpiresAt) {
			return nil, cache.ErrMiss
		}
	}
	return raw, err
}

func parseMenuInput(raw string) (resource.MenuInput, error) {
	input := resource.MenuInput{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return input, resource.ErrInvalid
	}
	for key, values := range values {
		if (key != "parent_id" && key != "depth") || len(values) != 1 {
			return input, resource.ErrInvalid
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n <= 0 {
			return input, resource.ErrInvalid
		}
		if key == "parent_id" {
			id := resource.ID(n)
			input.ParentID = &id
		} else {
			if int64(int(n)) != n {
				return input, resource.ErrInvalid
			}
			input.Depth = int(n)
		}
	}
	return input, nil
}

func (r *Runtime) serveMenu(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	runtime, ok := SiteRuntimeFromContext(ctx)
	actor, actorOK := httptransport.ActorFromContext(ctx)
	if !ok || !actorOK {
		httptransport.WriteJSONError(response, 500, "internal_error", "request context unavailable")
		return
	}
	input, err := parseMenuInput(request.URL.RawQuery)
	if err == nil {
		err = r.authorization.Check(ctx, actor, permission.MustCode("core", "resource", permission.Read))
	}
	var result resource.MenuResult
	if err == nil {
		result, err = r.menu(ctx, runtime, input)
	}
	if err != nil {
		status, code, message := 500, "internal_error", "menu unavailable"
		switch {
		case errors.Is(err, resource.ErrInvalid):
			status, code, message = 400, "invalid_query", "parent_id and depth must be positive integers"
		case errors.Is(err, resource.ErrNotFound):
			status, code, message = 404, "not_found", "menu parent not found"
		case errors.Is(err, security.ErrForbidden):
			status, code, message = 403, "forbidden", "resource access denied"
		case errors.Is(err, security.ErrUnauthenticated):
			status, code, message = 401, "unauthenticated", "authentication required"
		default:
			if r.logger != nil {
				r.logger.ErrorContext(ctx, "menu failed", "error", err)
			}
		}
		httptransport.WriteJSONError(response, status, code, message)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(result)
}

func (r *Runtime) menu(ctx context.Context, runtime *site.Runtime, input resource.MenuInput) (resource.MenuResult, error) {
	repository, ok := r.database.Resources().(resource.MenuRepository)
	if !ok {
		return resource.MenuResult{}, errors.New("menu repository unavailable")
	}
	return r.cachedMenu(ctx, runtime.Site().ID, input, func(ctx context.Context, now time.Time) (resource.MenuResult, *time.Time, error) {
		return resource.BuildMenu(ctx, repository, runtime, input, now)
	})
}

func (r *Runtime) cachedMenu(ctx context.Context, siteID site.ID, input resource.MenuInput, build func(context.Context, time.Time) (resource.MenuResult, *time.Time, error)) (resource.MenuResult, error) {
	parent := "root"
	if input.ParentID != nil {
		parent = strconv.FormatInt(int64(*input.ParentID), 10)
	}
	key := fmt.Sprintf("menu:v1:site:%d:parent:%s:depth:%d", siteID, parent, input.Depth)
	tags := []cache.Tag{siteTag(siteID), siteResourcesTag(siteID)}
	var store cache.Store
	if r.menuStore != nil {
		store = menuDeadlineStore{Store: r.menuStore, now: time.Now, logger: r.logger}
	}
	envelope, err := withRepositoryCacheRead(r.services.cachePolicy, []cache.Tag{siteResourcesTag(siteID)}, func() (menuEnvelope, error) {
		return cache.RememberJSONWithOptions(ctx, store, key, tags, func(value menuEnvelope) cache.SetOptions {
			ttl := time.Until(value.ExpiresAt)
			if ttl <= 0 {
				ttl = time.Nanosecond
			}
			return cache.SetOptions{TTL: ttl, Tags: tags}
		}, func(ctx context.Context) (menuEnvelope, error) {
			now := time.Now()
			result, next, err := build(ctx, now)
			expires := now.Add(r.menuTTL)
			if next != nil && next.Before(expires) {
				expires = *next
			}
			return menuEnvelope{Result: result, ExpiresAt: expires}, err
		})
	})
	return envelope.Result, err
}

func menuRecords(ctx context.Context, base resource.Repository, siteID site.ID, input resource.MenuInput) ([]resource.MenuRecord, error) {
	repository, ok := base.(resource.MenuRepository)
	if !ok {
		return nil, errors.New("menu repository unavailable")
	}
	return repository.MenuRecords(ctx, siteID, input)
}
func (r *cachedResourceRepository) MenuRecords(ctx context.Context, siteID site.ID, input resource.MenuInput) ([]resource.MenuRecord, error) {
	return menuRecords(ctx, r.base, siteID, input)
}
func (r *invalidatingResourceRepository) MenuRecords(ctx context.Context, siteID site.ID, input resource.MenuInput) ([]resource.MenuRecord, error) {
	return menuRecords(ctx, r.base, siteID, input)
}
