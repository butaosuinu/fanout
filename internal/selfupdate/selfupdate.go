// Package selfupdate compares the running fanout version with release tags.
package selfupdate

import (
	"strconv"
	"strings"
)

type Outcome int

const (
	DevBuild Outcome = iota
	UpToDate
	UpdateAvailable
	CurrentAhead
	CannotCompare
)

func (o Outcome) String() string {
	switch o {
	case DevBuild:
		return "dev build"
	case UpToDate:
		return "up to date"
	case UpdateAvailable:
		return "update available"
	case CurrentAhead:
		return "current ahead"
	case CannotCompare:
		return "cannot compare"
	default:
		return "unknown"
	}
}

// Compare returns the relationship between current and latest release tags.
// It accepts exactly MAJOR.MINOR.PATCH, optionally prefixed with "v".
func Compare(current, latest string) Outcome {
	if strings.TrimSpace(current) == "dev" {
		return DevBuild
	}

	cur, ok := parseVersion(current)
	if !ok {
		return CannotCompare
	}
	rel, ok := parseVersion(latest)
	if !ok {
		return CannotCompare
	}

	for i := range cur {
		switch {
		case cur[i] < rel[i]:
			return UpdateAvailable
		case cur[i] > rel[i]:
			return CurrentAhead
		}
	}
	return UpToDate
}

func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if part == "" {
			return out, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return out, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
