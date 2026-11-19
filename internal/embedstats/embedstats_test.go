package embedstats

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want Freshness
	}{
		{0, FreshnessFresh},
		{29*time.Minute + 59*time.Second, FreshnessFresh},
		{30 * time.Minute, FreshnessAged},
		{119 * time.Minute, FreshnessAged},
		{120 * time.Minute, FreshnessIdle},
		{24 * time.Hour, FreshnessIdle},
	}
	for _, c := range cases {
		if got := Classify(c.age); got != c.want {
			t.Errorf("Classify(%v) = %q, want %q", c.age, got, c.want)
		}
	}
}

func TestLevel(t *testing.T) {
	cases := []struct {
		tokens int
		f      Freshness
		want   string
	}{
		{1000, FreshnessFresh, ""},
		{255999, FreshnessFresh, ""},
		{256000, FreshnessFresh, "warn"},
		{511999, FreshnessFresh, "warn"},
		{512000, FreshnessFresh, "bad"},
		{900000, FreshnessFresh, "bad"},
		// 过期档强制不着色：旧值再大也不报警。
		{900000, FreshnessAged, ""},
		{900000, FreshnessIdle, ""},
	}
	for _, c := range cases {
		if got := Level(c.tokens, c.f); got != c.want {
			t.Errorf("Level(%d, %q) = %q, want %q", c.tokens, c.f, got, c.want)
		}
	}
}

func TestAgeMinutes(t *testing.T) {
	if got := AgeMinutes(42*time.Minute + 30*time.Second); got != 42 {
		t.Errorf("AgeMinutes = %d, want 42", got)
	}
}
