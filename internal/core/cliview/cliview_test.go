package cliview

import "testing"

func TestTableLine(t *testing.T) {
	tests := []struct {
		name   string
		cols   []string
		widths []int
		want   string
	}{
		{
			name:   "pads every column but the last",
			cols:   []string{"a", "bb", "c"},
			widths: []int{3, 4, 10},
			want:   "a    bb    c",
		},
		{
			name:   "single column is emitted unpadded",
			cols:   []string{"only"},
			widths: []int{10},
			want:   "only",
		},
		{
			name:   "column at exact width gains no padding",
			cols:   []string{"abc", "x"},
			widths: []int{3, 1},
			want:   "abc  x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TableLine(tt.cols, tt.widths); got != tt.want {
				t.Fatalf("TableLine(%q, %v) = %q, want %q", tt.cols, tt.widths, got, tt.want)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{name: "pads short value to width", s: "ab", width: 4, want: "ab  "},
		{name: "exact width adds nothing", s: "abcd", width: 4, want: "abcd"},
		{name: "value longer than width passes through", s: "abcdef", width: 4, want: "abcdef"},
		{name: "empty value becomes all spaces", s: "", width: 3, want: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PadRight(tt.s, tt.width); got != tt.want {
				t.Fatalf("PadRight(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestColorWrap(t *testing.T) {
	tests := []struct {
		name  string
		start string
		reset string
		s     string
		want  string
	}{
		{name: "wraps text in start and reset", start: "\x1b[32m", reset: "\x1b[0m", s: "ok", want: "\x1b[32mok\x1b[0m"},
		{name: "empty color passes text through", start: "", reset: "\x1b[0m", s: "ok", want: "ok"},
		{name: "empty text stays empty", start: "\x1b[32m", reset: "\x1b[0m", s: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ColorWrap(tt.start, tt.reset, tt.s); got != tt.want {
				t.Fatalf("ColorWrap(%q, %q, %q) = %q, want %q", tt.start, tt.reset, tt.s, got, tt.want)
			}
		})
	}
}

func TestColorPad(t *testing.T) {
	tests := []struct {
		name  string
		start string
		reset string
		s     string
		width int
		want  string
	}{
		{
			// escape sequences must not count toward the pad width
			name:  "pads by text width not colored width",
			start: "\x1b[32m",
			reset: "\x1b[0m",
			s:     "+3",
			width: 4,
			want:  "\x1b[32m+3\x1b[0m  ",
		},
		{name: "no color still pads", start: "", reset: "", s: "-", width: 3, want: "-  "},
		{name: "empty text pads to full width uncolored", start: "\x1b[31m", reset: "\x1b[0m", s: "", width: 2, want: "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ColorPad(tt.start, tt.reset, tt.s, tt.width); got != tt.want {
				t.Fatalf("ColorPad(%q, %q, %q, %d) = %q, want %q", tt.start, tt.reset, tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestDashIfEmpty(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want string
	}{
		{name: "empty becomes dash", s: "", want: "-"},
		{name: "non-empty passes through", s: "pass", want: "pass"},
		{name: "whitespace is not treated as empty", s: " ", want: " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DashIfEmpty(tt.s); got != tt.want {
				t.Fatalf("DashIfEmpty(%q) = %q, want %q", tt.s, got, tt.want)
			}
		})
	}
}
