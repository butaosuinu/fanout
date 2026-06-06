package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodexPlanShimStartsPlanTurnAndPrintsCompletedPlan(t *testing.T) {
	codex := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
case "$*" in
  "app-server") ;;
  *) echo "unexpected codex args: $*" >&2; exit 41 ;;
esac
read init_request
printf '%s\n' '{"id":"fanout-init","result":{"userAgent":"fake","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}'
read initialized_notification
read modes_request
printf '%s\n' '{"id":"fanout-modes","result":{"data":[{"name":"Plan","mode":"plan","reasoning_effort":"medium"},{"name":"Default","mode":"default","reasoning_effort":null}]}}'
read config_request
case "$config_request" in
  *'"method":"config/read"'*) ;;
  *) echo "missing config/read request" >&2; exit 45 ;;
esac
printf '%s\n' '{"id":"fanout-config","result":{"config":{"model":"gpt-test"},"origins":{},"layers":null}}'
read thread_request
case "$thread_request" in
  *'"model":"gpt-test"'*) ;;
  *) echo "missing thread model" >&2; exit 46 ;;
esac
case "$thread_request" in
  *'"approvalPolicy":"never"'*) ;;
  *) echo "missing thread approval policy" >&2; exit 47 ;;
esac
case "$thread_request" in
  *'"sandbox":"read-only"'*) ;;
  *) echo "missing thread read-only sandbox" >&2; exit 48 ;;
esac
printf '%s\n' '{"id":"fanout-thread","result":{"thread":{"id":"thread-1","sessionId":"session-1","modelProvider":"openai"}}}'
read turn_request
case "$turn_request" in
  *'"collaborationMode":{"mode":"plan"'*) ;;
  *) echo "missing plan collaboration mode" >&2; exit 42 ;;
esac
case "$turn_request" in
  *'"model":"gpt-test"'*) ;;
  *) echo "missing resolved model" >&2; exit 43 ;;
esac
case "$turn_request" in
  *'"approvalPolicy":"never"'*) ;;
  *) echo "missing turn approval policy" >&2; exit 49 ;;
esac
case "$turn_request" in
  *'"sandboxPolicy":{"networkAccess":false,"type":"readOnly"}'*) ;;
  *) echo "missing turn read-only sandbox policy" >&2; exit 50 ;;
esac
case "$turn_request" in
  *'"text":"[fanout #1] plan"'*) ;;
  *) echo "missing prompt text" >&2; exit 44 ;;
esac
printf '%s\n' '{"id":"fanout-turn","result":{"turn":{"id":"turn-1","items":[],"itemsView":"complete","status":"inProgress","error":null,"startedAt":1,"completedAt":null,"durationMs":null}}}'
printf '%s\n' '{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"cmd-1","command":["git","status"],"cwd":"/tmp/repo"}}'
read approval_response
case "$approval_response" in
  *'"id":"approval-1"'*'"decision":"decline"'*) ;;
  *) echo "missing command approval decline" >&2; exit 51 ;;
esac
printf '%s\n' '{"method":"item/plan/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"plan-1","delta":"draft plan"}}'
printf '%s\n' '{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"temporary disconnect","additionalDetails":"retrying"}}}'
printf '%s\n' '{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","items":[{"type":"plan","id":"plan-1","text":"final plan"}],"itemsView":"complete","status":"completed","error":null,"startedAt":1,"completedAt":2,"durationMs":1}}}'
`
	if err := os.WriteFile(codex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runCodexPlanShim(codexPlanShimConfig{
		CodexPath: codex,
		Prompt:    "[fanout #1] plan",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runCodexPlanShim() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if got := stdout.String(); got != "final plan\n" {
		t.Fatalf("stdout = %q, want completed plan with newline", got)
	}
	if strings.Contains(stderr.String(), "missing") {
		t.Fatalf("fake app-server rejected request:\n%s", stderr.String())
	}
}

func TestCodexPlanEffortRequiresPlanPreset(t *testing.T) {
	_, err := codexPlanEffort([]byte(`{"data":[{"name":"Default","mode":"default"}]}`))
	if err == nil || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("codexPlanEffort() error = %v, want missing plan preset", err)
	}
}
