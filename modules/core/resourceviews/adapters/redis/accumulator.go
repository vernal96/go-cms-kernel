// Package redis implements the resource-view accumulator using atomic Redis
// scripts. All participating CMS instances must share Redis DB and Prefix.
package redis

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type Factory struct {
	Options goredis.UniversalOptions
	Prefix  string
}

func (f Factory) Open(ctx context.Context) (resourceviews.Accumulator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(f.Options.Addrs) == 0 || strings.TrimSpace(f.Prefix) == "" || strings.ContainsAny(f.Prefix, "{}") || f.Options.DB < 0 {
		return nil, errors.New("invalid resource views Redis configuration")
	}
	options := f.Options
	options.Addrs = append([]string(nil), f.Options.Addrs...)
	// Bounded failure latency: views are secondary to the public response.
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	if options.DialTimeout == 0 {
		options.DialTimeout = 200 * time.Millisecond
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = 200 * time.Millisecond
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = 200 * time.Millisecond
	}
	return &Accumulator{options: options, prefix: f.Prefix}, nil
}

type Accumulator struct {
	options goredis.UniversalOptions
	prefix  string
	mu      sync.Mutex
	client  goredis.UniversalClient
	closed  bool
}

func (a *Accumulator) connection() (goredis.UniversalClient, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("resource views accumulator closed")
	}
	if a.client == nil {
		a.client = goredis.NewUniversalClient(&a.options)
	}
	return a.client, nil
}

func (a *Accumulator) keys(siteID site.ID) []string {
	base := "{" + a.prefix + ":" + strconv.FormatInt(int64(siteID), 10) + "}"
	return []string{base + ":pending", base + ":batch", base + ":id", base + ":lease"}
}

func (a *Accumulator) Increment(ctx context.Context, siteID site.ID, resourceID resource.ID) error {
	if siteID <= 0 || resourceID <= 0 {
		return errors.New("invalid resource views identity")
	}
	client, err := a.connection()
	if err != nil {
		return err
	}
	return client.HIncrBy(ctx, a.keys(siteID)[0], strconv.FormatInt(int64(resourceID), 10), 1).Err()
}

const claimScript = `
if not redis.call('SET', KEYS[4], ARGV[1], 'NX', 'PX', ARGV[2]) then return {} end
if redis.call('EXISTS', KEYS[2]) == 0 then
 local scan = redis.call('HSCAN', KEYS[1], 0, 'COUNT', 1000)
 while #scan[2] == 0 and scan[1] ~= '0' do scan = redis.call('HSCAN', KEYS[1], scan[1], 'COUNT', 1000) end
 local values = scan[2]
 if #values == 0 then redis.call('DEL', KEYS[4]); return {} end
 for i=1, math.min(#values, 2000), 2 do
  redis.call('HSET', KEYS[2], values[i], values[i+1])
  redis.call('HDEL', KEYS[1], values[i])
 end
 redis.call('SET', KEYS[3], ARGV[3])
end
local result = {redis.call('GET', KEYS[3])}
local values = redis.call('HGETALL', KEYS[2])
for i=1,#values do result[#result+1] = values[i] end
return result
`

func (a *Accumulator) Claim(ctx context.Context, siteID site.ID) (*resourceviews.Batch, error) {
	if siteID <= 0 {
		return nil, errors.New("invalid resource views site")
	}
	client, err := a.connection()
	if err != nil {
		return nil, err
	}
	owner := rand.Text()
	result, err := client.Eval(ctx, claimScript, a.keys(siteID), owner, 30000, rand.Text()).StringSlice()
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil
	}
	if len(result) < 3 || len(result)%2 != 1 {
		return nil, errors.New("corrupt resource views Redis batch")
	}
	batch := &resourceviews.Batch{ID: result[0], SiteID: siteID, Owner: owner, Counts: make(map[resource.ID]int64)}
	for i := 1; i < len(result); i += 2 {
		id, err := strconv.ParseInt(result[i], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid resource views resource id %q", result[i])
		}
		count, err := strconv.ParseInt(result[i+1], 10, 64)
		if err != nil || count <= 0 {
			return nil, fmt.Errorf("invalid resource views count %q", result[i+1])
		}
		batch.Counts[resource.ID(id)] = count
	}
	return batch, nil
}

const acknowledgeScript = `
if redis.call('GET', KEYS[4]) ~= ARGV[1] or redis.call('GET', KEYS[3]) ~= ARGV[2] then return 0 end
redis.call('DEL', KEYS[2], KEYS[3], KEYS[4])
return 1
`

func (a *Accumulator) Acknowledge(ctx context.Context, batch resourceviews.Batch) error {
	client, err := a.connection()
	if err != nil {
		return err
	}
	result, err := client.Eval(ctx, acknowledgeScript, a.keys(batch.SiteID), batch.Owner, batch.ID).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return errors.New("resource views batch lease lost")
	}
	return nil
}

func (a *Accumulator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.client != nil {
		return a.client.Close()
	}
	return nil
}

var _ resourceviews.Factory = Factory{}
var _ resourceviews.Accumulator = (*Accumulator)(nil)
