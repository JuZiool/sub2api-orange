package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Keep pool keys outside the legacy account-slot namespace so legacy sweeps
// cannot parse a proxy slot as an account ID. The aggregate account slot remains
// populated for scheduling load and the existing administrative metrics.
func accountProxySlotKey(accountID, proxyID int64) string {
	return fmt.Sprintf("concurrency:proxy_pool:{%d}:proxy:%d", accountID, proxyID)
}

var acquireProxyPoolScript = redis.NewScript(`
redis.replicate_commands()
local limit, ttl, request = tonumber(ARGV[1]), tonumber(ARGV[2]), ARGV[3]
local pinned = tonumber(ARGV[4])
local now = tonumber(redis.call('TIME')[1])
local n = #KEYS - 3
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now-ttl)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now-60)
local last = redis.call('GET', KEYS[3])
local start = 1
for i=1,n do
    redis.call('ZREMRANGEBYSCORE', KEYS[i+3], '-inf', now-ttl)
    if redis.call('ZSCORE', KEYS[i+3], request) ~= false then
        redis.call('ZADD', KEYS[i+3], now, request)
        redis.call('EXPIRE', KEYS[i+3], ttl)
        redis.call('ZADD', KEYS[1], now, request)
        redis.call('EXPIRE', KEYS[1], ttl)
        return {tonumber(ARGV[i+4]), now}
    end
    if last == ARGV[i+4] then start = i % n + 1 end
end
-- Also account for sessions acquired before the pool was changed.
if limit > 0 and redis.call('ZCARD', KEYS[1]) + redis.call('ZCARD', KEYS[2]) >= limit*n then
    return {0, now}
end
for offset=0,n-1 do
    local i = (start+offset-1) % n + 1
    local key = KEYS[i+3]
    if (pinned == 0 or pinned == tonumber(ARGV[i+4])) and (limit <= 0 or redis.call('ZCARD', key) < limit) then
        redis.call('ZADD', key, now, request)
        redis.call('EXPIRE', key, ttl)
        redis.call('ZADD', KEYS[1], now, request)
        redis.call('EXPIRE', KEYS[1], ttl)
        redis.call('SET', KEYS[3], ARGV[i+4], 'EX', ttl)
        return {tonumber(ARGV[i+4]), now}
    end
end
return {0, now}
`)

func (c *concurrencyCache) AcquireAccountProxyPoolSlot(ctx context.Context, accountID int64, proxyIDs []int64, maxConcurrency int, requestID string, pinnedProxyIDs ...int64) (int64, error) {
	if len(proxyIDs) == 0 {
		return 0, fmt.Errorf("proxy pool requires an available proxy")
	}
	keys := []string{accountSlotKey(accountID), liveAccountSlotKey(accountID), fmt.Sprintf("concurrency:proxy_pool:{%d}:cursor", accountID)}
	pinned := int64(0)
	if len(pinnedProxyIDs) > 0 {
		pinned = pinnedProxyIDs[0]
	}
	args := []any{maxConcurrency, c.slotTTLSeconds, requestID, pinned}
	seen := make(map[int64]bool, len(proxyIDs))
	for _, id := range proxyIDs {
		if id <= 0 || seen[id] {
			return 0, fmt.Errorf("invalid proxy pool ID %d", id)
		}
		seen[id] = true
		keys = append(keys, accountProxySlotKey(accountID, id))
		args = append(args, strconv.FormatInt(id, 10))
	}
	selected, now, err := runScriptInt64Pair(ctx, c.rdb, acquireProxyPoolScript, keys, args...)
	if err == nil && selected > 0 {
		c.touchActiveIndexAt(ctx, accountActiveIndexKey, accountID, now+int64(c.slotTTLSeconds))
	}
	return selected, err
}

var releaseProxyPoolScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`)

func (c *concurrencyCache) ReleaseAccountProxyPoolSlot(ctx context.Context, accountID, proxyID int64, requestID string) error {
	if err := releaseProxyPoolScript.Run(ctx, c.rdb, []string{accountSlotKey(accountID), accountProxySlotKey(accountID, proxyID)}, requestID).Err(); err != nil {
		return err
	}
	c.refreshAccountActiveIndex(ctx, accountID)
	return nil
}

func (c *concurrencyCache) GetAccountProxyConcurrencyBatch(ctx context.Context, pools map[int64][]int64) (map[int64]map[int64]int, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]map[int64]*redis.IntCmd)
	for aid, ids := range pools {
		cmds[aid] = make(map[int64]*redis.IntCmd)
		for _, id := range ids {
			cmds[aid][id] = pipe.ZCount(ctx, accountProxySlotKey(aid, id), fmt.Sprintf("(%d", now.Unix()-int64(c.slotTTLSeconds)), "+inf")
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make(map[int64]map[int64]int)
	for aid, items := range cmds {
		out[aid] = make(map[int64]int)
		for id, cmd := range items {
			out[aid][id] = int(cmd.Val())
		}
	}
	return out, nil
}
