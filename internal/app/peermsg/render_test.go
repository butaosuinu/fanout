package peermsg

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
)

func TestWriteMsgMessagesTable(t *testing.T) {
	to := 70
	read := "2026-06-13T00:00:00Z"
	msgs := []msgstore.Message{
		{ID: 1, From: 71, To: &to, Kind: "note", Body: "multi\nline", CreatedAt: "2026-06-13T00:00:00Z", ReadAt: &read},
		{ID: 3, From: 71, Board: true, Kind: "note", Body: "board post", CreatedAt: "2026-06-13T00:00:00Z"},
	}
	var out strings.Builder
	lg := log.NewWith(&out, &strings.Builder{}, false)
	writeMsgMessagesTable(msgs, true, nil, lg)
	got := out.String()
	for _, want := range []string{
		"ID  FROM  TO     KIND  CREATED               BODY",
		"1   #71   #70    note  2026-06-13T00:00:00Z  multi line",
		"3   #71   board  note  2026-06-13T00:00:00Z  board post",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q in:\n%s", want, got)
		}
	}

	out.Reset()
	writeMsgMessagesTable(nil, false, nil, lg)
	if got := out.String(); got != "no unread messages\n" {
		t.Errorf("empty unread table = %q", got)
	}
	out.Reset()
	writeMsgMessagesTable(nil, true, nil, lg)
	if got := out.String(); got != "no messages\n" {
		t.Errorf("empty all table = %q", got)
	}
}

// TestWriteMsgMessagesTablePlanLabels checks that a plan label map renders
// synthetic peer numbers as task ids in the FROM/TO columns.
func TestWriteMsgMessagesTablePlanLabels(t *testing.T) {
	apiNum := -111
	dbNum := -222
	msgs := []msgstore.Message{
		{ID: 1, From: dbNum, To: &apiNum, Kind: "note", Body: "ping", CreatedAt: "2026-06-13T00:00:00Z"},
	}
	labels := map[int]string{apiNum: "api-client", dbNum: "db-layer"}
	var out strings.Builder
	lg := log.NewWith(&out, &strings.Builder{}, false)
	writeMsgMessagesTable(msgs, true, labels, lg)
	got := out.String()
	if !strings.Contains(got, "db-layer") || !strings.Contains(got, "api-client") {
		t.Errorf("plan label table missing task ids in:\n%s", got)
	}
	if strings.Contains(got, "#-111") || strings.Contains(got, "#-222") {
		t.Errorf("plan label table leaked synthetic numbers in:\n%s", got)
	}
}
