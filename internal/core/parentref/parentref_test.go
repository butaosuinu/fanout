package parentref

import "testing"

// TestCanon pins the Atoi round-trip rule shared by state parent matching,
// sessionview aggregation, and the TUI issue keys.
func TestCanon(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		want   string
	}{
		{name: "strips leading zeros from numeric parent", parent: "0100", want: "100"},
		{name: "plain numeric parent unchanged", parent: "100", want: "100"},
		{name: "all-zero string collapses to single zero", parent: "000", want: "0"},
		{name: "negative number round-trips", parent: "-5", want: "-5"},
		{name: "plus sign is normalized away by Atoi", parent: "+7", want: "7"},
		{name: "empty string passes through", parent: "", want: ""},
		{name: "projects URL passes through", parent: "https://github.com/o/r/projects/3", want: "https://github.com/o/r/projects/3"},
		{name: "plan slug passes through", parent: "plan:my-slug", want: "plan:my-slug"},
		{name: "manual marker passes through", parent: "@manual", want: "@manual"},
		{name: "whitespace-padded number is not trimmed", parent: " 100", want: " 100"},
		// Out-of-range digit strings fail Atoi and pass through — same as the
		// pre-extraction NormalizeParent behavior.
		{name: "int-overflowing digits pass through", parent: "99999999999999999999", want: "99999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Canon(tt.parent); got != tt.want {
				t.Errorf("Canon(%q) = %q, want %q", tt.parent, got, tt.want)
			}
		})
	}
}
