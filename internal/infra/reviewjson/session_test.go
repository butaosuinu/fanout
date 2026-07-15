package reviewjson

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	childID  = "019f6259-a638-7e30-a971-b5700a8f6e0a"
	parentID = "019f6257-8e6e-7a00-8604-74db2394d370"
	otherID  = "019f6258-0000-7000-8000-000000000001"
	reserved = "2026-07-15T10:00:00Z"
)

type rolloutFixture struct {
	version, role, parent, sandbox, approval, task, created, result string
	completes                                                       int
	encrypted, inherited                                            bool
}

func validFixture(bundle, version string) rolloutFixture {
	return rolloutFixture{version, "post-work-reviewer", parentID, "read-only", "never", bundle, "2026-07-15T10:00:01Z", `{"reviewer_session_id":"` + childID + `","findings":[]}`, 1, false, false}
}

func writeRollout(t *testing.T, root string, f rolloutFixture) {
	t.Helper()
	role := fmt.Sprintf("%q", f.role)
	if f.role == "" {
		role = "null"
	}
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"parent_thread_id":%q,"timestamp":%q,"thread_source":"subagent","agent_role":%s,"multi_agent_version":%q,"source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"agent_role":%s}}}}}`, childID, f.parent, f.created, role, f.version, f.parent, role)
	task := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":%q}}`, f.task)
	if f.version == "v2" {
		content := fmt.Sprintf(`{"type":"input_text","text":%q}`, f.task)
		if f.encrypted {
			content = `{"type":"encrypted_content","encrypted_content":"opaque"}`
		}
		task = `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"runtime context"},` + content + `]}}`
	}
	if f.inherited {
		task = `{"type":"event_msg","payload":{"type":"user_message","message":"inherited"}}` + "\n" + task
	}
	context := fmt.Sprintf(`{"type":"turn_context","payload":{"sandbox_policy":{"type":%q},"approval_policy":%q}}`, f.sandbox, f.approval)
	complete := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":%q}}`+"\n", f.result)
	data := meta + "\n" + task + "\n" + context + "\n" + strings.Repeat(complete, f.completes)
	if err := os.WriteFile(filepath.Join(root, "rollout-"+childID+".jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureSessionAcceptsNativeV1AndV2(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			bundle := filepath.Join(root, "bundle.md")
			output := filepath.Join(root, "result.json")
			fixture := validFixture(bundle, version)
			writeRollout(t, root, fixture)
			if err := CaptureSession(root, childID, parentID, reserved, fixture.role, bundle, output); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output)
			info, statErr := os.Stat(output)
			if err != nil || statErr != nil || string(data) != fixture.result || info.Mode().Perm() != 0o600 {
				t.Fatalf("result=%q mode=%v read=%v stat=%v", data, info.Mode().Perm(), err, statErr)
			}
			if err := CaptureSession(root, childID, parentID, reserved, fixture.role, bundle, output); err == nil {
				t.Fatal("existing output was replaced")
			}
		})
	}
}

func TestCaptureSessionFailsClosed(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*rolloutFixture)
	}{
		{"null role", "agent role", func(f *rolloutFixture) { f.role = "" }},
		{"parent", "parent session", func(f *rolloutFixture) { f.parent = otherID }},
		{"stale", "not fresh", func(f *rolloutFixture) { f.created = reserved }},
		{"sandbox", "read-only", func(f *rolloutFixture) { f.sandbox = "workspace-write" }},
		{"approval", "read-only", func(f *rolloutFixture) { f.approval = "on-request" }},
		{"path", "bundle path", func(f *rolloutFixture) { f.task += ".other" }},
		{"encrypted", "bundle path", func(f *rolloutFixture) { f.version, f.encrypted = "v2", true }},
		{"inherited history", "bundle path", func(f *rolloutFixture) { f.inherited = true }},
		{"missing complete", "terminal metadata", func(f *rolloutFixture) { f.completes = 0 }},
		{"duplicate complete", "terminal metadata", func(f *rolloutFixture) { f.completes = 2 }},
		{"malformed result", "result is invalid", func(f *rolloutFixture) { f.result = "{" }},
		{"result UUID", "does not match", func(f *rolloutFixture) { f.result = `{"reviewer_session_id":"` + otherID + `"}` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bundle := filepath.Join(root, "bundle.md")
			fixture := validFixture(bundle, "v1")
			test.change(&fixture)
			writeRollout(t, root, fixture)
			err := CaptureSession(root, childID, parentID, reserved, "post-work-reviewer", bundle, filepath.Join(root, "out"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
	if err := CaptureSession(t.TempDir(), strings.ToUpper(childID), parentID, reserved, "post-work-reviewer", "/tmp/bundle", "/tmp/out"); err == nil {
		t.Fatal("noncanonical child UUID was accepted")
	}
}
