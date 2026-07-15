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
	project := len(args) == 2
	session := len(args) == 8 && args[0] == "session"
	if !project && !session {
		fmt.Fprintf(stderr, "%s: expected <review-json-file> <cache-dir> or session <sessions-root> <child-id> <parent-id> <reserved-at> <role> <bundle-path> <result-file>\n", postWorkReviewJSONCommand)
		return exitcode.Invocation
	}
	// Emit the protocol version before parsing so the driver can distinguish a
	// supported helper rejecting JSON from an old fanout binary that does not
	// recognize this hidden command.
	fmt.Fprintln(stdout, postWorkReviewJSONVersionLine)
	var err error
	if project {
		err = reviewjson.Project(args[0], args[1])
	} else {
		err = reviewjson.CaptureSession(args[1], args[2], args[3], args[4], args[5], args[6], args[7])
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", postWorkReviewJSONCommand, err)
		return exitcode.Env
	}
	return exitcode.OK
}
