// Package naming provides deterministic worktree and branch names for fanout.
package naming

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	DefaultBranchPrefix          = "fanout/"
	MaxSlugLength                = 80
	MaxHerdrSessionNameLength    = 56
	herdrSessionNamePrefix       = "fanout-"
	herdrSessionNameHashLength   = 16
	herdrSessionNameFallbackRepo = "repo"
)

// HerdrSessionName returns the stable per-repository name for a fanout-owned
// herdr session. gitCommonDir must be the canonical git common directory so
// linked worktrees resolve to the same session while independent clones do not.
func HerdrSessionName(gitCommonDir string) string {
	repoToken := herdrRepoToken(gitCommonDir)
	sum := sha256.Sum256([]byte(gitCommonDir))
	hash := hex.EncodeToString(sum[:])[:herdrSessionNameHashLength]

	maxRepoTokenLength := MaxHerdrSessionNameLength - len(herdrSessionNamePrefix) - 1 - len(hash)
	if len(repoToken) > maxRepoTokenLength {
		repoToken = strings.Trim(repoToken[:maxRepoTokenLength], "-")
		if repoToken == "" {
			repoToken = herdrSessionNameFallbackRepo
		}
	}

	return herdrSessionNamePrefix + repoToken + "-" + hash
}

func herdrRepoToken(gitCommonDir string) string {
	commonDir := filepath.Clean(gitCommonDir)
	repoName := filepath.Base(commonDir)
	if repoName == ".git" {
		repoName = filepath.Base(filepath.Dir(commonDir))
	} else {
		repoName = strings.TrimSuffix(repoName, ".git")
	}

	token := Slugify(repoName)
	if token == "" {
		return herdrSessionNameFallbackRepo
	}
	return token
}

// Slug returns a deterministic slug for an issue title and number.
func Slug(title string, num int) string {
	base := Slugify(title)
	if base == "" {
		base = "issue"
	}
	suffix := fmt.Sprintf("-%d", num)
	maxBase := max(MaxSlugLength-len(suffix), 1)
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

// QualifySlugForParent keeps shared child issues from colliding when the same
// issue is fanned from multiple parents or Projects.
func QualifySlugForParent(slug, parentRef string, issueNum int) string {
	base := strings.Trim(slug, "-")
	suffix := fmt.Sprintf("-%d", issueNum)
	base = strings.TrimSuffix(base, suffix)
	if base == "" {
		base = "issue"
	}
	parentToken := parentToken(parentRef)
	extra := "-" + parentToken + suffix
	maxBase := max(MaxSlugLength-len(extra), 1)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "issue"
		}
	}
	return base + extra
}

func parentToken(parentRef string) string {
	if AllDigits(parentRef) {
		return "parent-" + parentRef
	}
	token := Slugify(parentRef)
	if token == "" {
		return "parent-" + shortHash(parentRef)
	}
	if len(token) <= 32 {
		return token
	}
	token = strings.Trim(token[:23], "-")
	if token == "" {
		token = "parent"
	}
	return token + "-" + shortHash(parentRef)
}

// AllDigits reports whether s is non-empty and every rune is an ASCII digit.
// It preserves leading zeros (it never parses the number), matching the
// dashboard's `^\d+$` parent classification in web/src/lib/github.ts.
func AllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
