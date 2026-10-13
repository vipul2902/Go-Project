-- GCRA rate limit check. Runs atomically inside Redis.
-- KEYS[1]: rate limit key
-- ARGV[1]: emission interval in ms (period / limit): spacing between requests
-- ARGV[2]: burst (= limit): requests allowed back-to-back from idle
-- Returns {allowed (0|1), remaining, retry_after_ms, reset_after_ms}
--
-- Redis's own clock is used (not the caller's), so API replicas with
-- skewed clocks still agree on time.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local tolerance = interval * burst

-- TAT: the theoretical arrival time of the next request if traffic were
-- perfectly spaced. An idle key starts at "now".
local tat = tonumber(redis.call('GET', KEYS[1]))
if tat == nil or tat < now then
  tat = now
end

local new_tat = tat + interval
local allow_at = new_tat - tolerance
if allow_at > now then
  return {0, 0, allow_at - now, tat - now}
end

-- The key expires exactly when the bucket would be full again: idle keys
-- cost no memory, and expiry is safe because an absent key means "full".
redis.call('SET', KEYS[1], new_tat, 'PX', new_tat - now)
local remaining = math.floor((tolerance - (new_tat - now)) / interval)
return {1, remaining, 0, new_tat - now}
