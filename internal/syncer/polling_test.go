package syncer

import (
	"testing"
	"time"
)

func TestParsePolling(t *testing.T) {
	ok := map[string]Polling{
		"manual":  {Manual: true},
		" Manual": {Manual: true},
		"1m":      {Interval: time.Minute},
		"2m":      {Interval: 2 * time.Minute},
		"90s":     {Interval: 90 * time.Second},
		"15m":     {Interval: 15 * time.Minute},
	}
	for in, want := range ok {
		got, err := ParsePolling(in)
		if err != nil || got != want {
			t.Errorf("ParsePolling(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "30s", "59s", "16m", "1h", "often", "-2m"} {
		if p, err := ParsePolling(in); err == nil {
			t.Errorf("ParsePolling(%q) = %+v, want error", in, p)
		}
	}
}

func TestPollingString(t *testing.T) {
	for p, want := range map[Polling]string{
		{Manual: true}:               "manual",
		{Interval: 2 * time.Minute}:  "2m",
		{Interval: 15 * time.Minute}: "15m",
		{Interval: 90 * time.Second}: "1m30s",
		DefaultPolling():             "2m",
	} {
		if got := p.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", p, got, want)
		}
	}
}
