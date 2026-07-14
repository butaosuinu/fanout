package main

import (
	"fmt"
	"io"
	"time"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/reviewjson"
)

const (
	postWorkReviewJSONCommand     = "__post-work-review-json"
	postWorkReviewJSONVersionLine = "post_work_review_json_helper_version=5"
	postWorkReviewTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"
)

func isPostWorkReviewJSONRequest(args []string) bool {
	return len(args) > 0 && args[0] == postWorkReviewJSONCommand
}

func cmdPostWorkReviewJSON(args []string, stdout, stderr io.Writer) exitcode.Code {
	// Emit the protocol version before parsing so the driver can distinguish a
	// supported helper rejecting JSON from an old fanout binary that does not
	// recognize this hidden command.
	fmt.Fprintln(stdout, postWorkReviewJSONVersionLine)
	if len(args) == 3 && args[0] == "project" {
		if err := reviewjson.Project(args[1], args[2]); err != nil {
			fmt.Fprintf(stderr, "%s project: %v\n", postWorkReviewJSONCommand, err)
			return exitcode.Env
		}
		return exitcode.OK
	}
	if len(args) == 1 && args[0] == "timestamp" {
		fmt.Fprintf(stdout, "timestamp=%s\n", time.Now().UTC().Format(postWorkReviewTimestampLayout))
		return exitcode.OK
	}
	if len(args) == 2 && args[0] == "digest" {
		digest, err := reviewjson.BundleSHA256(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "%s digest: %v\n", postWorkReviewJSONCommand, err)
			return exitcode.Env
		}
		fmt.Fprintf(stdout, "bundle_sha256=%s\n", digest)
		return exitcode.OK
	}
	if len(args) == 3 && args[0] == "controller" {
		controller, err := reviewjson.AttestController(args[1], args[2])
		if err != nil {
			fmt.Fprintf(stderr, "%s controller: %v\n", postWorkReviewJSONCommand, err)
			return exitcode.Env
		}
		fmt.Fprintf(stdout, "review_controller_turn_id=%s\n", controller.TurnID)
		fmt.Fprintf(stdout, "review_controller_context_sha256=%s\n", controller.ContextSHA256)
		fmt.Fprintf(stdout, "review_controller_sandbox_mode=%s\n", controller.SandboxMode)
		return exitcode.OK
	}
	if len(args) == 4 && args[0] == "extract" {
		sessionID, err := reviewjson.ExtractLastAgentMessage(args[1], args[2], args[3])
		if err != nil {
			fmt.Fprintf(stderr, "%s extract: %v\n", postWorkReviewJSONCommand, err)
			return exitcode.Env
		}
		fmt.Fprintf(stdout, "extracted_session_id=%s\n", sessionID)
		return exitcode.OK
	}
	if (len(args) == 11 || len(args) == 12) && args[0] == "attest" {
		usedSessionIDsPath := ""
		if len(args) == 12 {
			usedSessionIDsPath = args[11]
		}
		if err := reviewjson.Attest(
			args[1],
			args[2],
			args[3],
			args[4],
			args[5],
			args[6],
			args[7],
			args[8],
			args[9],
			args[10],
			usedSessionIDsPath,
		); err != nil {
			fmt.Fprintf(stderr, "%s attest: %v\n", postWorkReviewJSONCommand, err)
			return exitcode.Env
		}
		return exitcode.OK
	}
	fprintfPostWorkReviewJSONUsage(stderr)
	return exitcode.Invocation
}

func fprintfPostWorkReviewJSONUsage(stderr io.Writer) {
	fmt.Fprintf(
		stderr,
		"%s: expected timestamp, digest <bundle-file>, project <review-json-file> <cache-dir>, controller <sessions-root> <parent-thread-id>, extract <sessions-root> <session-id> <output-file>, or attest <review-json-file> <cache-dir> <sessions-root> <parent-thread-id> <prepared-at> <agent-toml> <expected-bundle-path> <expected-controller-turn-id> <expected-controller-context-sha256> <spawn-authorized-at> [<used-session-ids-file>]\n",
		postWorkReviewJSONCommand,
	)
}
