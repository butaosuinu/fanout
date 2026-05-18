package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

func TestPrintSummaryUsesInvokedCommandNameInLimitRerunHint(t *testing.T) {
	var out, err bytes.Buffer
	lg := log.NewWith(&out, &err, false)
	plan := Plan{
		LimitDeferred: []ghissue.Issue{{Number: 702}},
	}
	cfg := &cliflags.Config{
		ParentRef: "700",
		Agent:     "claude",
	}

	printSummary(plan, executionResult{}, cfg, lg, log.Palette{}, "fanout-go")

	got := out.String()
	if !strings.Contains(got, "  fanout-go 700 --limit 1 --agent claude\n") {
		t.Fatalf("summary output did not include fanout-go rerun hint:\n%s", got)
	}
	if strings.Contains(got, "  fanout 700 --limit") {
		t.Fatalf("summary output fell back to fanout:\n%s", got)
	}
}
