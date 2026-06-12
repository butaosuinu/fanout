package team

import (
	"strings"
	"testing"
	"time"
)

func TestNow(t *testing.T) {
	tests := []struct {
		name string
		fake string
		want string
	}{
		{name: "fake now returned verbatim", fake: "2026-01-02T03:04:05Z", want: "2026-01-02T03:04:05Z"},
		{name: "fake now is not validated", fake: "not-a-time", want: "not-a-time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(FakeNowEnv, tt.fake)
			if got := Now(); got != tt.want {
				t.Errorf("Now() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNowRealIsRFC3339UTC(t *testing.T) {
	t.Setenv(FakeNowEnv, "")
	before := time.Now().UTC().Add(-time.Minute)
	got := Now()
	after := time.Now().UTC().Add(time.Minute)

	if !strings.HasSuffix(got, "Z") {
		t.Errorf("Now() = %q, want UTC timestamp ending in Z", got)
	}
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("Now() = %q is not RFC3339: %v", got, err)
	}
	if parsed.Before(before) || parsed.After(after) {
		t.Errorf("Now() = %q outside [%v, %v]", got, before, after)
	}
}
