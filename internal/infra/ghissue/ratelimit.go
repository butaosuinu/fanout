package ghissue

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrRateLimited identifies GitHub API failures while the shared gh cooldown
// gate is active.
var ErrRateLimited = errors.New("github API rate limited")

const (
	rateLimitInitialBackoff = 30 * time.Second
	rateLimitMaxBackoff     = 15 * time.Minute
)

var (
	rateLimitHTTPPattern  = regexp.MustCompile(`(?i)\bHTTP\s+(?:403|429)\b`)
	rateLimitTextPattern  = regexp.MustCompile(`(?i)\b(?:API rate limit (?:already )?exceeded|secondary rate limit|abuse detection mechanism)\b`)
	rateLimitResetPattern = regexp.MustCompile(`(?i)(?:x-ratelimit-reset|rate[-_ ]?limit[-_ ]?reset|reset[_ ]?at|resets?\s+at)["']?\s*[:=]?\s*["']?([0-9]{9,13}|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(?:Z|[+-][0-9]{2}:[0-9]{2}))`)
	retryAfterPattern     = regexp.MustCompile(`(?i)\bretry[- ]after["']?\s*[:=]?\s*["']?([0-9]+(?:\.[0-9]+)?)(?:\s*(milliseconds?|ms|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h))?`)

	sharedRateLimitGate = newRateLimitGate(time.Now)
)

type rateLimitGate struct {
	mu       sync.Mutex
	now      func() time.Time
	until    time.Time
	failures int
}

func newRateLimitGate(now func() time.Time) *rateLimitGate {
	return &rateLimitGate{now: now}
}

func (g *rateLimitGate) before() error {
	now := g.now()
	g.mu.Lock()
	until := g.until
	g.mu.Unlock()
	if !now.Before(until) {
		return nil
	}
	return newRateLimitedError(now, until, nil)
}

func (g *rateLimitGate) after(err error) error {
	now := g.now()
	if err == nil || !isRateLimitError(err) {
		g.recordRecovery(now)
		return err
	}

	resetAt, hasReset := rateLimitResetAt(err.Error(), now)

	g.mu.Lock()
	if now.Before(g.until) {
		if hasReset && resetAt.After(g.until) {
			g.until = resetAt
		}
	} else {
		g.failures++
		if !hasReset || !resetAt.After(now) {
			resetAt = now.Add(rateLimitBackoff(g.failures))
		}
		g.until = resetAt
	}
	until := g.until
	g.mu.Unlock()

	return newRateLimitedError(now, until, err)
}

func (g *rateLimitGate) recordRecovery(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until) {
		return
	}
	g.until = time.Time{}
	g.failures = 0
}

type rateLimitedError struct {
	observedAt time.Time
	until      time.Time
	cause      error
}

func newRateLimitedError(observedAt, until time.Time, cause error) error {
	return &rateLimitedError{
		observedAt: observedAt,
		until:      until,
		cause:      cause,
	}
}

func (e *rateLimitedError) Error() string {
	remaining := max(e.until.Sub(e.observedAt).Round(time.Second), time.Second)
	message := fmt.Sprintf(
		"%s: cooldown %s remaining (until %s)",
		ErrRateLimited,
		remaining,
		e.until.UTC().Format(time.RFC3339),
	)
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	return message
}

func (e *rateLimitedError) Unwrap() error {
	return ErrRateLimited
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return rateLimitHTTPPattern.MatchString(message) || rateLimitTextPattern.MatchString(message)
}

func rateLimitResetAt(message string, now time.Time) (time.Time, bool) {
	if match := rateLimitResetPattern.FindStringSubmatch(message); len(match) == 2 {
		if resetAt, ok := parseRateLimitReset(match[1]); ok {
			return resetAt, true
		}
	}

	match := retryAfterPattern.FindStringSubmatch(message)
	if len(match) != 3 {
		return time.Time{}, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return time.Time{}, false
	}
	unit, ok := retryAfterUnit(match[2])
	if !ok {
		return time.Time{}, false
	}
	return now.Add(time.Duration(value * float64(unit))), true
}

func parseRateLimitReset(value string) (time.Time, bool) {
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if len(value) >= 13 {
			return time.UnixMilli(unix), true
		}
		return time.Unix(unix, 0), true
	}
	resetAt, err := time.Parse(time.RFC3339Nano, value)
	return resetAt, err == nil
}

func retryAfterUnit(raw string) (time.Duration, bool) {
	switch strings.ToLower(raw) {
	case "", "s", "sec", "secs", "second", "seconds":
		return time.Second, true
	case "ms", "millisecond", "milliseconds":
		return time.Millisecond, true
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute, true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour, true
	default:
		return 0, false
	}
}

func rateLimitBackoff(failures int) time.Duration {
	backoff := rateLimitInitialBackoff
	for range max(failures-1, 0) {
		if backoff >= rateLimitMaxBackoff/2 {
			return rateLimitMaxBackoff
		}
		backoff *= 2
	}
	return min(backoff, rateLimitMaxBackoff)
}
