package main

import (
	"fmt"
	"io"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/reviewjson"
)

const (
	postWorkReviewJSONCommand     = "__post-work-review-json"
	postWorkReviewJSONVersionLine = "post_work_review_json_helper_version=2"
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
	if (len(args) == 7 || len(args) == 8) && args[0] == "attest" {
		usedSessionIDsPath := ""
		if len(args) == 8 {
			usedSessionIDsPath = args[7]
		}
		if err := reviewjson.Attest(
			args[1],
			args[2],
			args[3],
			args[4],
			args[5],
			args[6],
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
		"%s: expected project <review-json-file> <cache-dir> or attest <review-json-file> <cache-dir> <sessions-root> <parent-thread-id> <prepared-at> <agent-toml> [<used-session-ids-file>]\n",
		postWorkReviewJSONCommand,
	)
}
