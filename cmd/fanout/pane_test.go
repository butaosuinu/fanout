package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortIssueTitleTruncatesOnRuneBoundary(t *testing.T) {
	title := strings.Repeat("あ", 61)

	got := shortIssueTitle(title)

	if !utf8.ValidString(got) {
		t.Fatalf("short title is invalid UTF-8: %q", got)
	}
	if gotRunes := utf8.RuneCountInString(got); gotRunes != 60 {
		t.Fatalf("short title rune count = %d, want 60", gotRunes)
	}
	if got != strings.Repeat("あ", 60) {
		t.Fatalf("unexpected short title: %q", got)
	}
}

func TestShortIssueTitleKeepsSixtyRunes(t *testing.T) {
	title := strings.Repeat("界", 60)

	if got := shortIssueTitle(title); got != title {
		t.Fatalf("shortIssueTitle changed 60-rune title:\nwant %q\ngot  %q", title, got)
	}
}
