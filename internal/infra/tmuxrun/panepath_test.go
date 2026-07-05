package tmuxrun

import (
	"strings"
	"testing"
)

// TestPaneCurrentPath pins the display-message invocation and the two error
// strings that moved verbatim from cmd/fanout's tmuxPaneGitToplevel.
func TestPaneCurrentPath(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		want    string
		wantErr string
	}{
		{
			name:   "returns the trimmed pane path",
			script: "printf '%s\\n' \"$@\" > \"$TMUXRUN_ARGS\"\nprintf '/tmp/work tree\\n'\n",
			want:   "/tmp/work tree",
		},
		{
			name:    "tmux failure is wrapped with the fixed prefix",
			script:  "exit 1\n",
			wantErr: "tmux display-message pane_current_path: ",
		},
		{
			name:    "blank output reports the empty-path error",
			script:  "printf '  \\n'\n",
			wantErr: "tmux pane current path is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installTmuxShim(t, tt.script)
			got, err := PaneCurrentPath("%3")
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("PaneCurrentPath() error = %v, want prefix %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PaneCurrentPath() failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("PaneCurrentPath() = %q, want %q", got, tt.want)
			}
			assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%3", "#{pane_current_path}"})
		})
	}
}
