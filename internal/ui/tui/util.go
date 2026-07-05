package tui

import (
	"strings"
	"time"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonDashStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "-" || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
	}
	return out
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	return truncateRunes(s, maxLen)
}

func truncatePreserveSpace(s string, maxLen int) string {
	return truncateRunes(s, maxLen)
}

func fixedLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateRunes(s, width)
	if pad := width - len([]rune(s)); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 || len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func compactMessage(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return truncate(strings.Join(fields, " "), 160)
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
