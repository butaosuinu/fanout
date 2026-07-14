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
	postWorkReviewJSONVersionLine = "post_work_review_json_helper_version=3"
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
	if (len(args) == 8 || len(args) == 9) && args[0] == "attest" {
		usedSessionIDsPath := ""
		if len(args) == 9 {
			usedSessionIDsPath = args[8]
		}
		if err := reviewjson.Attest(
			args[1],
			args[2],
			args[3],
			args[4],
			args[5],
			args[6],
			args[7],
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
		"%s: expected timestamp, project <review-json-file> <cache-dir>, or attest <review-json-file> <cache-dir> <sessions-root> <parent-thread-id> <prepared-at> <agent-toml> <expected-bundle-path> [<used-session-ids-file>]\n",
		postWorkReviewJSONCommand,
	)
}
