package reviewjson

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testExtractSessionID = "019f5c78-2577-70f3-bc26-d6f83b2b5d75"

func TestExtractLastAgentMessagePreservesUnicode(t *testing.T) {
	t.Parallel()
	const message = "修正済み — JSONをそのまま保持\n次の行"
	sessionsRoot := t.TempDir()
	writeExtractRollout(t, sessionsRoot, testExtractSessionID, []string{message})
	outputPath := filepath.Join(t.TempDir(), "review.json")

	sessionID, err := ExtractLastAgentMessage(sessionsRoot, testExtractSessionID, outputPath)
	if err != nil {
		t.Fatalf("ExtractLastAgentMessage() error = %v", err)
	}
	if sessionID != testExtractSessionID {
		t.Errorf("session ID = %q, want %q", sessionID, testExtractSessionID)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != message {
		t.Errorf("extracted bytes = %q, want %q", data, message)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("output mode = %o, want 600", got)
	}
}

func TestExtractLastAgentMessageRejectsExistingDestination(t *testing.T) {
	t.Parallel()
	sessionsRoot := t.TempDir()
	writeExtractRollout(t, sessionsRoot, testExtractSessionID, []string{"clean"})
	tests := []struct {
		name    string
		arrange func(*testing.T, string)
	}{
		{
			name: "regular file",
			arrange: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			arrange: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputPath := filepath.Join(t.TempDir(), "review.json")
			test.arrange(t, outputPath)

			_, err := ExtractLastAgentMessage(sessionsRoot, testExtractSessionID, outputPath)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("ExtractLastAgentMessage() error = %v, want existing-path failure", err)
			}
		})
	}
}

func TestExtractLastAgentMessageRequiresOneTerminalMessage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		messages []string
		want     string
	}{
		{name: "missing", messages: nil, want: "0 task_complete"},
		{name: "duplicate", messages: []string{"one", "two"}, want: "2 task_complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sessionsRoot := t.TempDir()
			writeExtractRollout(t, sessionsRoot, testExtractSessionID, test.messages)
			outputPath := filepath.Join(t.TempDir(), "review.json")

			_, err := ExtractLastAgentMessage(sessionsRoot, testExtractSessionID, outputPath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExtractLastAgentMessage() error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("output exists after extraction failure: %v", statErr)
			}
		})
	}
}

func TestExtractLastAgentMessagePreservesReplacementCharacterAndSurrogatePair(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		messageToken string
		want         string
	}{
		{
			name:         "literal replacement character",
			messageToken: `"�"`,
			want:         "�",
		},
		{
			name:         "escaped replacement character",
			messageToken: `"\ufffd"`,
			want:         "�",
		},
		{
			name:         "valid surrogate pair",
			messageToken: `"\ud83d\ude00"`,
			want:         "😀",
		},
		{
			name:         "literal emoji",
			messageToken: `"😀"`,
			want:         "😀",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sessionsRoot := t.TempDir()
			writeExtractRolloutWithMessageToken(
				t,
				sessionsRoot,
				testExtractSessionID,
				test.messageToken,
			)
			outputPath := filepath.Join(t.TempDir(), "review.json")

			_, err := ExtractLastAgentMessage(sessionsRoot, testExtractSessionID, outputPath)
			if err != nil {
				t.Fatalf("ExtractLastAgentMessage() error = %v", err)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, []byte(test.want)) {
				t.Errorf("extracted bytes = % x, want % x", data, []byte(test.want))
			}
		})
	}
}

func TestExtractLastAgentMessageRejectsInvalidUTF8AndUnpairedSurrogates(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		rollout []byte
		want    string
	}{
		{
			name: "invalid UTF-8",
			rollout: append(
				[]byte(`{"type":"session_meta","payload":{"id":"`+testExtractSessionID+`"}}`+"\n"+
					`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"`),
				append([]byte{0xff}, []byte(`"}}`+"\n")...)...,
			),
			want: "not valid UTF-8",
		},
		{
			name: "lone high surrogate",
			rollout: []byte(
				`{"type":"session_meta","payload":{"id":"` + testExtractSessionID + `"}}` + "\n" +
					`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"\ud800"}}` + "\n",
			),
			want: "unpaired UTF-16 surrogate",
		},
		{
			name: "lone low surrogate",
			rollout: []byte(
				`{"type":"session_meta","payload":{"id":"` + testExtractSessionID + `"}}` + "\n" +
					`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"\udc00"}}` + "\n",
			),
			want: "unpaired UTF-16 surrogate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sessionsRoot := t.TempDir()
			dir := filepath.Join(sessionsRoot, "2026", "07", "13")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(
				dir,
				"rollout-2026-07-13T17-13-25-"+testExtractSessionID+".jsonl",
			)
			if err := os.WriteFile(path, test.rollout, 0o600); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(t.TempDir(), "review.json")

			_, err := ExtractLastAgentMessage(sessionsRoot, testExtractSessionID, outputPath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExtractLastAgentMessage() error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("output exists after extraction failure: %v", statErr)
			}
		})
	}
}

func writeExtractRolloutWithMessageToken(
	t *testing.T,
	sessionsRoot string,
	sessionID string,
	messageToken string,
) {
	t.Helper()
	dir := filepath.Join(sessionsRoot, "2026", "07", "13")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := []byte(
		`{"type":"session_meta","payload":{"id":"` + sessionID + `"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":` +
			messageToken + `}}` + "\n",
	)
	path := filepath.Join(dir, "rollout-2026-07-13T17-13-25-"+sessionID+".jsonl")
	if err := os.WriteFile(path, rollout, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExtractRollout(
	t *testing.T,
	sessionsRoot string,
	sessionID string,
	messages []string,
) {
	t.Helper()
	dir := filepath.Join(sessionsRoot, "2026", "07", "13")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	records := []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, sessionID),
	}
	for _, message := range messages {
		records = append(records, fmt.Sprintf(
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":%q}}`,
			message,
		))
	}
	path := filepath.Join(dir, "rollout-2026-07-13T17-13-25-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
