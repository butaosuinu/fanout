package tui

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestTranslateShiftEnterInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "kitty csi u", raw: "a\x1b[13;2ub", want: "a\nb"},
		{name: "kitty press event", raw: "a\x1b[13;2:1ub", want: "a\nb"},
		{name: "kitty repeat event", raw: "a\x1b[13;2:2ub", want: "a\nb"},
		{name: "kitty release event ignored", raw: "a\x1b[13;2:3ub", want: "ab"},
		{name: "kitty special enter", raw: "a\x1b[57345;2ub", want: "a\nb"},
		{name: "kitty special plain enter with caps lock", raw: "a\x1b[57345;65ub", want: "a\rb"},
		{name: "kitty special ctrl enter unchanged", raw: "a\x1b[57345;5ub", want: "a\x1b[57345;5ub"},
		{name: "xterm modify other keys", raw: "a\x1b[27;2;13~b", want: "a\nb"},
		{name: "kitty plain esc", raw: "a\x1b[27ub", want: "a\x1bb"},
		{name: "kitty plain enter", raw: "a\x1b[13ub", want: "a\rb"},
		{name: "kitty plain special enter", raw: "a\x1b[57345ub", want: "a\rb"},
		{name: "kitty plain tab", raw: "a\x1b[9ub", want: "a\tb"},
		{name: "kitty plain printable", raw: "a\x1b[65ub", want: "aAb"},
		{name: "kitty shifted letter", raw: "a\x1b[97;2ub", want: "aAb"},
		{name: "kitty shifted alternate code", raw: "a\x1b[97:65;2ub", want: "aAb"},
		{name: "kitty shifted associated text", raw: "a\x1b[97;2;65ub", want: "aAb"},
		{name: "kitty shifted digit associated text", raw: "a\x1b[50;2;34ub", want: "a\"b"},
		{name: "kitty shifted slash associated text", raw: "a\x1b[47;2;63ub", want: "a?b"},
		{name: "kitty shifted punctuation without text unchanged", raw: "a\x1b[50;2ub", want: "a\x1b[50;2ub"},
		{name: "kitty special up", raw: "a\x1b[57352ub", want: "a\x1b[Ab"},
		{name: "kitty special shift up", raw: "a\x1b[57352;2ub", want: "a\x1b[1;2Ab"},
		{name: "kitty special up with num lock", raw: "a\x1b[57352;129ub", want: "a\x1b[Ab"},
		{name: "kitty special shift up with num lock", raw: "a\x1b[57352;130ub", want: "a\x1b[1;2Ab"},
		{name: "kitty special down", raw: "a\x1b[57353ub", want: "a\x1b[Bb"},
		{name: "kitty special home", raw: "a\x1b[57356ub", want: "a\x1b[Hb"},
		{name: "kitty special end", raw: "a\x1b[57357ub", want: "a\x1b[Fb"},
		{name: "kitty special delete", raw: "a\x1b[57349ub", want: "a\x1b[3~b"},
		{name: "kitty special backspace", raw: "a\x1b[57347ub", want: "a\x7fb"},
		{name: "kitty special alt backspace unchanged", raw: "a\x1b[57347;3ub", want: "a\x1b[57347;3ub"},
		{name: "kitty special f5", raw: "a\x1b[57368ub", want: "a\x1b[15~b"},
		{name: "kitty unknown special unchanged", raw: "a\x1b[57358ub", want: "a\x1b[57358ub"},
		{name: "kitty ctrl c", raw: "a\x1b[99;5ub", want: "a\x03b"},
		{name: "kitty ctrl c with num lock", raw: "a\x1b[99;133ub", want: "a\x03b"},
		{name: "kitty ctrl c release ignored", raw: "a\x1b[99;5:3ub", want: "ab"},
		{name: "kitty ctrl u event", raw: "a\x1b[117;5:1ub", want: "a\x15b"},
		{name: "kitty ctrl j fallback", raw: "a\x1b[106;5ub", want: "a\nb"},
		{name: "xterm ctrl c", raw: "a\x1b[27;5;99~b", want: "a\x03b"},
		{name: "xterm ctrl j fallback", raw: "a\x1b[27;5;106~b", want: "a\nb"},
		{name: "kitty shift tab", raw: "a\x1b[9;2ub", want: "a\x1b[Zb"},
		{name: "kitty special shift tab", raw: "a\x1b[57346;2ub", want: "a\x1b[Zb"},
		{name: "xterm shift tab", raw: "a\x1b[27;2;9~b", want: "a\x1b[Zb"},
		{name: "xterm shifted printable", raw: "a\x1b[27;2;65~b", want: "aAb"},
		{name: "xterm shifted punctuation", raw: "a\x1b[27;2;63~b", want: "a?b"},
		{name: "plain enter unchanged", raw: "a\rb", want: "a\rb"},
		{name: "ctrl enter unchanged", raw: "a\x1b[13;5ub", want: "a\x1b[13;5ub"},
		{name: "other csi unchanged", raw: "a\x1b[1;2Ab", want: "a\x1b[1;2Ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pending := translateShiftEnterInput([]byte(tt.raw), false)
			if string(got) != tt.want || len(pending) != 0 {
				t.Fatalf("translateShiftEnterInput() = %q pending %q, want %q no pending", got, pending, tt.want)
			}
		})
	}
}

func TestTranslateShiftEnterInputKeepsSplitPrefix(t *testing.T) {
	got, pending := translateShiftEnterInput([]byte("a\x1b[13;"), false)
	if string(got) != "a" || string(pending) != "\x1b[13;" {
		t.Fatalf("first translate = %q pending %q, want a and split prefix", got, pending)
	}

	got, pending = translateShiftEnterInput(append(pending, []byte("2ub")...), false)
	if string(got) != "\nb" || len(pending) != 0 {
		t.Fatalf("second translate = %q pending %q, want newline+b and no pending", got, pending)
	}
}

func TestTranslateShiftEnterInputKeepsGenericCSIUPrefix(t *testing.T) {
	got, pending := translateShiftEnterInput([]byte("a\x1b["), false)
	if string(got) != "a" || string(pending) != "\x1b[" {
		t.Fatalf("first translate = %q pending %q, want a and generic CSI prefix", got, pending)
	}

	got, pending = translateShiftEnterInput(append(pending, []byte("113ub")...), false)
	if string(got) != "qb" || len(pending) != 0 {
		t.Fatalf("second translate = %q pending %q, want q+b and no pending", got, pending)
	}
}

func TestTranslateShiftEnterInputDoesNotBufferBareEsc(t *testing.T) {
	got, pending := translateShiftEnterInput([]byte("a\x1b"), false)
	if string(got) != "a\x1b" || len(pending) != 0 {
		t.Fatalf("translate bare esc = %q pending %q, want bare esc and no pending", got, pending)
	}
}

func TestTranslateShiftEnterInputFlushesIncompleteFinalPrefix(t *testing.T) {
	got, pending := translateShiftEnterInput([]byte("a\x1b[13;"), true)
	if string(got) != "a\x1b[13;" || len(pending) != 0 {
		t.Fatalf("final translate = %q pending %q, want original bytes and no pending", got, pending)
	}
}

func TestShiftEnterInputReadTranslatesAcrossReads(t *testing.T) {
	input := newShiftEnterInput(&chunkedFDReader{chunks: [][]byte{
		[]byte("a\x1b[13;"),
		[]byte("2ub"),
	}})

	var got []byte
	buf := make([]byte, 8)
	for {
		n, err := input.Read(buf)
		got = append(got, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(got) != "a\nb" {
		t.Fatalf("read all = %q, want a newline b", got)
	}
}

func TestShiftEnterInputPreservesFd(t *testing.T) {
	src := &chunkedFDReader{fd: 42, name: "/dev/test-tty"}
	input := newShiftEnterInput(src)
	if got := input.Fd(); got != 42 {
		t.Fatalf("Fd() = %d, want 42", got)
	}
	if got := input.Name(); got != "/dev/test-tty" {
		t.Fatalf("Name() = %q, want /dev/test-tty", got)
	}
}

type chunkedFDReader struct {
	chunks [][]byte
	fd     uintptr
	name   string
}

func (r *chunkedFDReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	if len(chunk) > len(p) {
		panic("test chunk too large")
	}
	return copy(p, chunk), nil
}

func (r *chunkedFDReader) Write(p []byte) (int, error) {
	return len(p), nil
}

func (r *chunkedFDReader) Close() error {
	return nil
}

func (r *chunkedFDReader) Fd() uintptr {
	return r.fd
}

func (r *chunkedFDReader) Name() string {
	return r.name
}

func TestShiftEnterInputSequencesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, seq := range shiftEnterInputSequences {
		if seen[string(seq)] {
			t.Fatalf("duplicate shift enter sequence %q", seq)
		}
		seen[string(seq)] = true
	}
	if !reflect.DeepEqual(shiftEnterInputSequences[0], []byte("\x1b[13;2u")) {
		t.Fatalf("first sequence = %q, want common Kitty CSI-u sequence first", shiftEnterInputSequences[0])
	}
}

func TestEnableShiftEnterInputRequestsKittyAllKeys(t *testing.T) {
	if !strings.Contains(enableShiftEnterInput, "\x1b[>29u") {
		t.Fatalf("enableShiftEnterInput = %q, want Kitty flags 1+4+8+16 pushed", enableShiftEnterInput)
	}
	if !strings.Contains(disableShiftEnterInput, "\x1b[<1u") {
		t.Fatalf("disableShiftEnterInput = %q, want Kitty keyboard mode pop", disableShiftEnterInput)
	}
}
