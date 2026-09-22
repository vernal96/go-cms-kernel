package widget

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
)

// ResultCacheable is an explicit promise that the instance's output is public
// and depends only on RenderInput, its parameters, and the declared tags.
// Request actors, headers and other context values must not affect that output.
type ResultCacheable interface {
	ResultCachePolicy(RenderInput) ResultCachePolicy
}
type ResultCachePolicy struct {
	TTL  time.Duration
	Tags []cache.Tag
}

// ResultCacheDeadline supplies a time boundary discovered while rendering, for
// example the next scheduled publication in a collection, including new items.
type ResultCacheDeadline interface{ ResultCacheDeadline() time.Time }

type RenderJob struct {
	Identity    string
	Fingerprint any
	Instance    Instance
	Input       RenderInput
}
type RenderResult struct {
	Data map[string]any
	Err  error
}
type resultEnvelope struct {
	Data      map[string]any
	ExpiresAt time.Time
}

// RenderBatch retains per-widget failure isolation and uses one batch lookup.
// generation identifies the compiled site runtime, so a reload cannot reuse
// output rendered by a previous module implementation or configuration.
func RenderBatch(ctx context.Context, store cache.Store, generation string, jobs []RenderJob) []RenderResult {
	result := make([]RenderResult, len(jobs))
	keys := make([]string, len(jobs))
	policies := make([]ResultCachePolicy, len(jobs))
	lookup := make([]string, 0, len(jobs))
	for index, job := range jobs {
		if configurable, ok := job.Instance.(ResultCacheable); ok && store != nil {
			policy := configurable.ResultCachePolicy(job.Input)
			if policy.TTL <= 0 {
				continue
			}
			raw, err := json.Marshal(struct {
				Generation, Identity string
				Input                RenderInput
				Fingerprint          any
			}{generation, job.Identity, job.Input, job.Fingerprint})
			if err != nil {
				result[index].Err = err
				continue
			}
			digest := sha256.Sum256(raw)
			keys[index] = fmt.Sprintf("widget:result:v1:%s:%x", job.Identity, digest)
			policies[index] = policy
			lookup = append(lookup, keys[index])
		}
	}
	values := cache.GetMany(ctx, store, lookup)
	for index, job := range jobs {
		if result[index].Err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			result[index].Err = err
			continue
		}
		key := keys[index]
		policy := policies[index]
		if key != "" {
			value := values[key]
			envelope, decoded := cache.DecodeCachedJSON[resultEnvelope](ctx, store, key, value)
			if decoded && envelope.Data != nil && time.Now().Before(envelope.ExpiresAt) {
				result[index].Data = envelope.Data
				continue
			}
		}
		var prepared cache.PreparedSet
		if key != "" {
			prepared = cache.Prepare(ctx, store, policy.Tags)
		}
		started := time.Now()
		data, err := job.Instance.Render(ctx, job.Input)
		result[index] = RenderResult{Data: data, Err: err}
		if err != nil || data == nil || key == "" || ctx.Err() != nil {
			continue
		}
		expires := started.Add(policy.TTL)
		if bounded, ok := job.Instance.(ResultCacheDeadline); ok {
			if deadline := bounded.ResultCacheDeadline(); !deadline.IsZero() && deadline.Before(expires) {
				expires = deadline
			}
		}
		ttl := time.Until(expires)
		if ttl <= 0 {
			continue
		}
		cache.WritePreparedJSON(ctx, store, prepared, key, resultEnvelope{Data: data, ExpiresAt: expires}, ttl)
	}
	return result
}
