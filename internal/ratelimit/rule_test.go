package ratelimit

import (
	"testing"
	"time"
)

func TestRuleValidate(t *testing.T) {
	for _, ok := range []Rule{
		{Name: "a", Limit: 5, Period: time.Minute},
		{Name: "fast", Limit: 10_000, Period: time.Second}, // 100µs: was broken with ms precision
		{Name: "huge", Limit: 1 << 30, Period: time.Hour},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for _, bad := range []Rule{
		{Name: "", Limit: 1, Period: time.Second},
		{Name: "zero", Limit: 0, Period: time.Second},
		{Name: "noperiod", Limit: 1},
		{Name: "toofine", Limit: 2_000_000, Period: time.Second}, // 0.5µs
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
