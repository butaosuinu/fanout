package tui

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
)

const (
	enableShiftEnterInput  = "\x1b[>1u\x1b[>4;2m"
	disableShiftEnterInput = "\x1b[<1u\x1b[>4;m"
)

var shiftEnterInputSequences = [][]byte{
	[]byte("\x1b[13;2u"),      // Kitty keyboard / CSI-u
	[]byte("\x1b[13;2:1u"),    // Kitty key press event
	[]byte("\x1b[13;2:2u"),    // Kitty repeat event
	[]byte("\x1b[57345;2u"),   // Kitty special enter key code
	[]byte("\x1b[57345;2:1u"), // Kitty special enter key press event
	[]byte("\x1b[57345;2:2u"), // Kitty special enter repeat event
	[]byte("\x1b[27;2;13~"),   // xterm modifyOtherKeys
}

type fdReader interface {
	io.ReadWriteCloser
	Fd() uintptr
	Name() string
}

type shiftEnterInput struct {
	src     fdReader
	pending []byte
	out     []byte
	err     error
}

var _ term.File = (*shiftEnterInput)(nil)

type keyboardProtocols interface {
	Enable()
	Disable()
}

type noopKeyboardProtocols struct{}

func (noopKeyboardProtocols) Enable()  {}
func (noopKeyboardProtocols) Disable() {}

type shiftEnterProtocols struct {
	output  *os.File
	mu      sync.Mutex
	enabled bool
}

func newShiftEnterInput(src fdReader) *shiftEnterInput {
	return &shiftEnterInput{src: src}
}

func newShiftEnterProgramInput(stdin *os.File) (*shiftEnterInput, func(), error) {
	if stdin != nil && term.IsTerminal(stdin.Fd()) {
		return newShiftEnterInput(stdin), func() {}, nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return nil, func() {}, err
	}
	return newShiftEnterInput(tty), func() {
		_ = tty.Close() // Best-effort close for the fallback TTY input.
	}, nil
}

func (r *shiftEnterInput) Fd() uintptr {
	return r.src.Fd()
}

func (r *shiftEnterInput) Name() string {
	return r.src.Name()
}

func (r *shiftEnterInput) Write(p []byte) (int, error) {
	return r.src.Write(p)
}

func (r *shiftEnterInput) Close() error {
	return nil
}

func (r *shiftEnterInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.out) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		var buf [256]byte
		n, err := r.src.Read(buf[:])
		if n > 0 {
			raw := make([]byte, 0, len(r.pending)+n)
			raw = append(raw, r.pending...)
			raw = append(raw, buf[:n]...)
			r.out, r.pending = translateShiftEnterInput(raw, err != nil)
		}
		if err != nil {
			r.err = err
		}
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func newShiftEnterProtocols(output *os.File) keyboardProtocols {
	if output == nil || !term.IsTerminal(output.Fd()) {
		return noopKeyboardProtocols{}
	}
	return &shiftEnterProtocols{output: output}
}

func (p *shiftEnterProtocols) Enable() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled {
		return
	}
	if _, err := p.output.WriteString(enableShiftEnterInput); err == nil {
		p.enabled = true
	}
}

func (p *shiftEnterProtocols) Disable() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return
	}
	_, _ = p.output.WriteString(disableShiftEnterInput) // Best-effort terminal cleanup.
	p.enabled = false
}

func translateShiftEnterInput(raw []byte, final bool) ([]byte, []byte) {
	out := make([]byte, 0, len(raw))
	for len(raw) > 0 {
		if replacement, width, ok, pending := translateKeyboardProtocolInput(raw, final); pending {
			return out, append([]byte(nil), raw...)
		} else if ok {
			out = append(out, replacement...)
			raw = raw[width:]
			continue
		}
		if !final && isShiftEnterSequencePrefix(raw) {
			return out, append([]byte(nil), raw...)
		}
		out = append(out, raw[0])
		raw = raw[1:]
	}
	return out, nil
}

func translateKeyboardProtocolInput(raw []byte, final bool) ([]byte, int, bool, bool) {
	if len(raw) == 1 && raw[0] == '\x1b' {
		return nil, 0, false, false
	}
	if !bytes.HasPrefix(raw, []byte("\x1b[")) {
		return nil, 0, false, false
	}
	end := keyboardProtocolSequenceEnd(raw)
	if end < 0 {
		if final || !isKeyboardProtocolSequencePrefix(raw) {
			return nil, 0, false, false
		}
		return nil, 0, false, true
	}

	seq := raw[:end+1]
	body := string(seq[2:end])
	var replacement []byte
	var ok bool
	switch seq[end] {
	case 'u':
		replacement, ok = translateCSIUSequence(body)
	case '~':
		replacement, ok = translateXtermModifyOtherKeysSequence(body)
	}
	if !ok {
		return seq, len(seq), true, false
	}
	return replacement, len(seq), true, false
}

func keyboardProtocolSequenceEnd(raw []byte) int {
	for i := 2; i < len(raw); i++ {
		switch raw[i] {
		case 'u', '~':
			return i
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ';', ':':
			continue
		default:
			return -1
		}
	}
	return -1
}

func isKeyboardProtocolSequencePrefix(raw []byte) bool {
	if len(raw) < 2 || !bytes.HasPrefix(raw, []byte("\x1b[")) {
		return false
	}
	for i := 2; i < len(raw); i++ {
		switch raw[i] {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ';', ':':
			continue
		default:
			return false
		}
	}
	return len(raw) > 2
}

func translateCSIUSequence(body string) ([]byte, bool) {
	parts := strings.SplitN(body, ";", 2)
	keyCode, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, false
	}
	if len(parts) != 2 {
		return unmodifiedKeyReplacement(keyCode)
	}
	modPart := parts[1]
	if before, _, ok := strings.Cut(modPart, ":"); ok {
		if eventType := keyboardProtocolEventType(modPart); eventType == "3" {
			return nil, true
		}
		modPart = before
	}
	modifier, err := strconv.Atoi(modPart)
	if err != nil {
		return nil, false
	}
	return modifiedKeyReplacement(keyCode, modifier)
}

func unmodifiedKeyReplacement(keyCode int) ([]byte, bool) {
	if keyCode >= 0 && keyCode <= 31 || keyCode == 127 {
		return []byte{byte(keyCode)}, true
	}
	if keyCode == 57345 {
		return []byte{'\r'}, true
	}
	if keyCode >= 32 && utf8.ValidRune(rune(keyCode)) {
		return []byte(string(rune(keyCode))), true
	}
	return nil, false
}

func keyboardProtocolEventType(modPart string) string {
	_, eventType, ok := strings.Cut(modPart, ":")
	if !ok {
		return ""
	}
	eventType, _, _ = strings.Cut(eventType, ";")
	return eventType
}

func translateXtermModifyOtherKeysSequence(body string) ([]byte, bool) {
	parts := strings.Split(body, ";")
	if len(parts) != 3 || parts[0] != "27" {
		return nil, false
	}
	modifier, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, false
	}
	keyCode, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, false
	}
	return modifiedKeyReplacement(keyCode, modifier)
}

func modifiedKeyReplacement(keyCode, modifier int) ([]byte, bool) {
	shift, alt, ctrl := keyboardModifierState(modifier)
	if shift && !alt && !ctrl && isEnterKeyCode(keyCode) {
		return []byte{'\n'}, true
	}
	if shift && !alt && !ctrl && keyCode == '\t' {
		return []byte("\x1b[Z"), true
	}
	if ctrl && !alt {
		if b, ok := controlByteForKeyCode(keyCode); ok {
			return []byte{b}, true
		}
	}
	if !alt && !ctrl && keyCode >= 32 && utf8.ValidRune(rune(keyCode)) {
		return []byte(string(rune(keyCode))), true
	}
	return nil, false
}

func keyboardModifierState(modifier int) (shift, alt, ctrl bool) {
	if modifier <= 0 {
		return false, false, false
	}
	bits := modifier - 1
	return bits&1 != 0, bits&2 != 0, bits&4 != 0
}

func isEnterKeyCode(keyCode int) bool {
	return keyCode == 13 || keyCode == 57345
}

func controlByteForKeyCode(keyCode int) (byte, bool) {
	switch {
	case keyCode == ' ' || keyCode == '@':
		return 0, true
	case keyCode >= 'a' && keyCode <= 'z':
		return byte(keyCode - 'a' + 1), true
	case keyCode >= 'A' && keyCode <= 'Z':
		return byte(keyCode - 'A' + 1), true
	case keyCode >= '[' && keyCode <= '_':
		return byte(keyCode - '@'), true
	case keyCode == '?':
		return 0x7f, true
	default:
		return 0, false
	}
}

func isShiftEnterSequencePrefix(raw []byte) bool {
	if len(raw) == 1 && raw[0] == '\x1b' {
		return false
	}
	for _, seq := range shiftEnterInputSequences {
		if len(raw) < len(seq) && bytes.HasPrefix(seq, raw) {
			return true
		}
	}
	return false
}
