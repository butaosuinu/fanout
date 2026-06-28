package panelayout

import "fmt"

// Checksum computes tmux's 16-bit window-layout checksum over body, the layout
// string without its leading "checksum," prefix. tmux rejects a custom layout
// passed to `select-layout` whose checksum does not match, so this must mirror
// tmux's algorithm exactly (right-rotate by one bit, add the byte, wrap to 16
// bits) and emit four lowercase hex digits.
//
// Layout strings are ASCII only (digits and the set "x,{}[]"), so iterating
// bytes is equivalent to tmux's per-character pass; never feed it non-ASCII.
func Checksum(body string) string {
	var c uint16
	for _, b := range []byte(body) {
		c = (c >> 1) + ((c & 1) << 15)
		c += uint16(b)
	}
	return fmt.Sprintf("%04x", c)
}
