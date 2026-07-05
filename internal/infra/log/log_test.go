package log

import "testing"

func TestPaletteForSelectsByTerminalCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		term      string
		colorterm string
		wantErr   string
	}{
		{name: "plain xterm falls back to 16-color", term: "xterm", wantErr: "\x1b[31m"},
		{name: "empty TERM falls back to 16-color", term: "", wantErr: "\x1b[31m"},
		{name: "xterm-256color picks 256-color palette", term: "xterm-256color", wantErr: "\x1b[38;5;167m"},
		{name: "tmux-256color picks 256-color palette", term: "tmux-256color", wantErr: "\x1b[38;5;167m"},
		{name: "COLORTERM=truecolor upgrades plain TERM", term: "xterm", colorterm: "truecolor", wantErr: "\x1b[38;5;167m"},
		{name: "COLORTERM=24bit upgrades plain TERM", term: "xterm-direct", colorterm: "24bit", wantErr: "\x1b[38;5;167m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := paletteFor(tc.term, tc.colorterm)
			if p.Err != tc.wantErr {
				t.Errorf("paletteFor(%q, %q).Err = %q, want %q", tc.term, tc.colorterm, p.Err, tc.wantErr)
			}
			if p.Reset != "\x1b[0m" || p.Dim != "\x1b[2m" {
				t.Errorf("paletteFor(%q, %q) Dim/Reset = %q/%q, want \\x1b[2m/\\x1b[0m", tc.term, tc.colorterm, p.Dim, p.Reset)
			}
		})
	}
}
