-- GCRA rate limit check. Runs atomically inside Redis.
-- KEYS[1]: rate limit key
-- ARGV[1]: emission interval in MICROseconds (period / limit)
-- ARGV[2]: burst (= limit): requests allowed back-to-back from idle
-- Returns {allowed (0|1), remaining, retry_after_ms, reset_after_ms}
--
-- Microsecond precision: with millisecond integers, fast rules (more than
-- one request per millisecond) computed an interval of 0, which made
-- "SET ... PX 0" fail and the limiter fail open (found by a benchmark).
-- Redis's own clock is used, so API replicas with skewed clocks agree.
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local tolerance = interval * burst

-- TAT: the theoretical arrival time of the next request if traffic were
-- perfectly spaced. An idle key starts at "now".
local tat = tonumber(redis.call('GET', KEYS[1]))
if tat == nil or tat < now then
  tat = now
end

local function ms(us) return math.ceil(us / 1000) end

local new_tat = tat + interval
local allow_at = new_tat - tolerance
if allow_at > now then
  return {0, 0, ms(allow_at - now), ms(tat - now)}
end

-- The key expires exactly when the bucket would be full again: idle keys
-- cost no memory, and expiry is safe because an absent key means "full".
redis.call('SET', KEYS[1], string.format('%.0f', new_tat), 'PX', math.max(1, ms(new_tat - now)))
local remaining = math.floor((tolerance - (new_tat - now)) / interval)
return {1, remaining, 0, ms(new_tat - now)}
