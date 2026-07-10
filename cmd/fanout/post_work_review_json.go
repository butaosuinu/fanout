package main

import (
	"fmt"
	"io"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/reviewjson"
)

const (
	postWorkReviewJSONCommand     = "__post-work-review-json"
	postWorkReviewJSONVersionLine = "post_work_review_json_helper_version=1"
)

func isPostWorkReviewJSONRequest(args []string) bool {
	return len(args) > 0 && args[0] == postWorkReviewJSONCommand
}

func cmdPostWorkReviewJSON(args []string, stdout, stderr io.Writer) exitcode.Code {
	if len(args) != 2 {
		fmt.Fprintf(stderr, "%s: expected <review-json-file> <cache-dir>\n", postWorkReviewJSONCommand)
		return exitcode.Invocation
	}
	// Emit the protocol version before parsing so the driver can distinguish a
	// supported helper rejecting JSON from an old fanout binary that does not
	// recognize this hidden command.
	fmt.Fprintln(stdout, postWorkReviewJSONVersionLine)
	if err := reviewjson.Project(args[0], args[1]); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", postWorkReviewJSONCommand, err)
		return exitcode.Env
	}
	return exitcode.OK
}
