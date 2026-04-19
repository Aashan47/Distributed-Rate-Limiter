-- Sliding Window Log rate limiter.
-- KEYS[1]  : sorted-set key for this (client, rule) pair.
-- ARGV[1]  : now in milliseconds
-- ARGV[2]  : window size in milliseconds
-- ARGV[3]  : limit (max requests per window)
-- ARGV[4]  : cost (number of tokens this call consumes, >= 1)
-- ARGV[5]  : unique request id (uuid)
--
-- Returns {allowed, remaining, reset_at_ms, retry_after_ms}.
-- allowed       : 1 if admitted, 0 if denied
-- remaining     : tokens left in the current window after this call (0 if denied)
-- reset_at_ms   : epoch ms when the oldest tracked request leaves the window
-- retry_after_ms: 0 on allow; ms until caller should retry on deny

local key      = KEYS[1]
local now      = tonumber(ARGV[1])
local window   = tonumber(ARGV[2])
local limit    = tonumber(ARGV[3])
local cost     = tonumber(ARGV[4])
local req_id   = ARGV[5]

-- Prune entries outside the window (inclusive on the lower bound).
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)

local count = redis.call('ZCARD', key)

if count + cost <= limit then
    for i = 1, cost do
        -- Member must be unique per unit of cost so ZADD does not collapse duplicates.
        redis.call('ZADD', key, now, req_id .. ':' .. i)
    end
    redis.call('PEXPIRE', key, window)
    local remaining = limit - count - cost
    local reset_at  = now + window
    return {1, remaining, reset_at, 0}
end

-- Denied: find the oldest tracked request so we can tell the caller when to retry.
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retry_after = 1
local reset_at    = now + window
if #oldest >= 2 then
    local oldest_score = tonumber(oldest[2])
    reset_at    = oldest_score + window
    retry_after = reset_at - now
    if retry_after < 1 then retry_after = 1 end
end
return {0, 0, reset_at, retry_after}
