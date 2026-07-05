package tmuxrun

import "testing"

func TestDisplayMessageUsesTmuxStatusLine(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)
	if err := DisplayMessage("fanout", "fanout: #(touch /tmp/pwned)"); err != nil {
		t.Fatalf("DisplayMessage: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-t", "=fanout", "fanout: ##(touch /tmp/pwned)"})
}

func TestDisplayMessageToClientTargetsClientStatusLine(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)
	if err := DisplayMessageToClient("/dev/ttys001", "fanout: #(touch /tmp/pwned)"); err != nil {
		t.Fatalf("DisplayMessageToClient: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-c", "/dev/ttys001", "fanout: ##(touch /tmp/pwned)"})
}
