package herdrrun

import (
	"os"
	"testing"
)

func TestOwnerOnlySocketModes(t *testing.T) {
	for _, tt := range []struct {
		mode os.FileMode
		want bool
	}{
		{mode: 0o600, want: true},
		{mode: 0o700, want: true},
		{mode: 0o660, want: false},
		{mode: 0o707, want: false},
	} {
		if got := isOwnerOnlySocketMode(tt.mode); got != tt.want {
			t.Errorf("isOwnerOnlySocketMode(%#o) = %t, want %t", tt.mode, got, tt.want)
		}
	}
}
