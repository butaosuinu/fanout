// Package naming provides deterministic worktree and branch names for fanout.
package naming

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	DefaultBranchPrefix = "fanout/"
	MaxSlugLength       = 80
)

// Slug returns a deterministic slug for an issue title and number.
func Slug(title string, num int) string {
	base := Slugify(title)
	if base == "" {
		base = "issue"
	}
	suffix := fmt.Sprintf("-%d", num)
	maxBase := MaxSlugLength - len(suffix)
	if maxBase < 1 {
		maxBase = 1
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "issue"
		}
	}
	return base + suffix
}

// Slugify converts a title to lowercase ASCII kebab-case.
func Slugify(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/' || r == ':' || r == '.':
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// BranchName returns the branch override or the default fanout branch name.
func BranchName(override, prefix, slug string) string {
	if override != "" {
		return override
	}
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}
	return prefix + slug
}

func EnsureIssueSuffix(slug string, issueNum int) string {
	base := strings.Trim(slug, "-")
	if base == "" {
		return fmt.Sprintf("issue-%d", issueNum)
	}
	suffix := fmt.Sprintf("-%d", issueNum)
	if strings.HasSuffix(base, suffix) {
		return base
	}
	return base + suffix
}
