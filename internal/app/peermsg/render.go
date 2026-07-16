package peermsg

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/cliview"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

type msgMessagesReport struct {
	Self int `json:"self"`
	// SelfTask, FromTask, and ToTask are populated only for plan parents, where
	// the numeric members are negative synthetic peer numbers; they carry the
	// task ids automation actually addresses by. omitempty keeps issue/Project
	// JSON byte-identical.
	SelfTask   string               `json:"selfTask,omitempty"`
	Parent     string               `json:"parent"`
	All        bool                 `json:"all"`
	Messages   []msgMessageView     `json:"messages"`
	MarkedRead *msgstore.MarkResult `json:"marked_read,omitempty"`
}

// msgMessageView is a message plus, for plan parents, the task ids of its
// from/to members. The embedded Message promotes its fields to the top level,
// so issue-mode JSON (empty FromTask/ToTask) matches the bare Message encoding.
type msgMessageView struct {
	msgstore.Message
	FromTask string `json:"fromTask,omitempty"`
	ToTask   string `json:"toTask,omitempty"`
}

type msgPeersReport struct {
	Parent string          `json:"parent"`
	Peers  []msgstore.Peer `json:"peers"`
}

type msgMarkReadReport struct {
	Self        int     `json:"self"`
	SelfTask    string  `json:"selfTask,omitempty"`
	MarkedIDs   []int64 `json:"marked_ids"`
	BoardCursor *int64  `json:"board_cursor,omitempty"`
}

type msgRegisterReport struct {
	Peer msgstore.Peer `json:"peer"`
}

// msgSendView is the JSON encoding of a send/post echo: the raw message for
// issue/Project parents (byte-identical), or a task-id-enriched view for plan
// parents so a sending automation can reuse fromTask/toTask from the response
// without reverse-engineering the synthetic numbers. fromTask is the sender's
// task id (the resolved pane's TaskID); toTask is the recipient's (the raw --to
// token), unset for board posts (msg.To == nil).
func msgSendView(msg msgstore.Message, parent, fromTask, toTask string) any {
	if !team.IsPlanParent(parent) {
		return msg
	}
	v := msgMessageView{Message: msg, FromTask: fromTask}
	if msg.To != nil {
		v.ToTask = toTask
	}
	return v
}

// msgMessageViews attaches plan task ids to each message's from/to from labels.
// With a nil labels map (issue/Project parents) the views carry no task ids.
func msgMessageViews(msgs []msgstore.Message, labels map[int]string) []msgMessageView {
	views := make([]msgMessageView, len(msgs))
	for i, m := range msgs {
		v := msgMessageView{Message: m}
		v.FromTask = labels[m.From]
		if m.To != nil {
			v.ToTask = labels[*m.To]
		}
		views[i] = v
	}
	return views
}

// writeMsgMessages is the shared renderer for the inbox and board views.
// marked is non-nil only for `inbox --mark-read`. For a plan parent it maps the
// synthetic peer numbers in From/To back to task ids for a readable table.
func writeMsgMessages(req *Request, store *msgstore.Store, self int, parent string, msgs []msgstore.Message, marked *msgstore.MarkResult, lg *log.Logger) exitcode.Code {
	// labels is nil for issue/Project parents (no peers query, no task ids) and
	// maps synthetic numbers to task ids for plan parents — used by both the
	// JSON enrichment and the human table.
	labels, code := msgMemberLabels(store, req.Verb, parent, lg)
	if code != exitcode.OK {
		return code
	}
	if req.JSON {
		return writeMsgJSON(msgMessagesReport{
			Self:       self,
			SelfTask:   labels[self],
			Parent:     parent,
			All:        req.All,
			Messages:   msgMessageViews(msgs, labels),
			MarkedRead: marked,
		}, lg)
	}
	writeMsgMessagesTable(msgs, req.All, labels, lg)
	if marked != nil {
		lg.Ok("marked %d message(s) read, board cursor at %d", len(marked.MessageIDs), marked.BoardCursor)
	}
	return exitcode.OK
}

// msgMemberLabels maps synthetic peer numbers to plan task ids so inbox/board
// rows render readable FROM/TO. It returns nil for non-plan parents, so numeric
// issue/Project views keep rendering "#<n>" with no extra DB read.
func msgMemberLabels(store *msgstore.Store, verb, parent string, lg *log.Logger) (map[int]string, exitcode.Code) {
	labels, err := planPeerLabels(store, parent)
	if err != nil {
		return nil, msgBackendErr(verb, err, lg)
	}
	return labels, exitcode.OK
}

// planPeerLabels is msgMemberLabels without the logging: the error-returning
// form Watcher.Poll needs (it has no exit code to produce). nil for non-plan
// parents.
func planPeerLabels(store *msgstore.Store, parent string) (map[int]string, error) {
	if !team.IsPlanParent(parent) {
		return nil, nil
	}
	peers, err := store.Peers()
	if err != nil {
		return nil, err
	}
	labels := make(map[int]string, len(peers))
	for _, p := range peers {
		if p.TaskID != "" {
			labels[p.Issue] = p.TaskID
		}
	}
	return labels, nil
}

// memberDisplay renders a peer number as its plan task id when known, else as
// "#<n>". A nil labels map (issue/Project parents) always yields "#<n>".
func memberDisplay(num int, labels map[int]string) string {
	if id, ok := labels[num]; ok {
		return id
	}
	return "#" + strconv.Itoa(num)
}

// peerDisplayLabel is the human label for a registered peer: the task id for a
// plan-task peer, "#<issue>" for a numeric issue peer.
func peerDisplayLabel(p msgstore.Peer) string {
	if p.TaskID != "" {
		return p.TaskID
	}
	return "#" + strconv.Itoa(p.Issue)
}

func writeMsgResult(req *Request, report any, summary string, lg *log.Logger) exitcode.Code {
	if req.JSON {
		return writeMsgJSON(report, lg)
	}
	lg.Ok("%s", summary)
	return exitcode.OK
}

func writeMsgJSON(report any, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("msg: failed to encode report: %v", err)
		return exitcode.Backend
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

func writeMsgMessagesTable(msgs []msgstore.Message, all bool, labels map[int]string, lg *log.Logger) {
	out := lg.Stdout()
	if len(msgs) == 0 {
		if all {
			fmt.Fprintln(out, "no messages")
		} else {
			fmt.Fprintln(out, "no unread messages")
		}
		return
	}
	headers := []string{"ID", "FROM", "TO", "KIND", "CREATED", "BODY"}
	rows := make([][]string, 0, len(msgs))
	for _, m := range msgs {
		to := "board"
		if m.To != nil {
			to = memberDisplay(*m.To, labels)
		}
		rows = append(rows, []string{
			strconv.FormatInt(m.ID, 10),
			memberDisplay(m.From, labels),
			to,
			m.Kind,
			m.CreatedAt,
			msgTableBody(m.Body),
		})
	}
	colors := lg.Colors()
	dims := make([]bool, len(msgs))
	for i, m := range msgs {
		dims[i] = m.ReadAt != nil
	}
	writeMsgTable(out, headers, rows, dims, colors)
}

func writeMsgPeersTable(peers []msgstore.Peer, lg *log.Logger) {
	out := lg.Stdout()
	if len(peers) == 0 {
		fmt.Fprintln(out, "no peers registered")
		return
	}
	headers := []string{"PEER", "SLUG", "AGENT", "DISPLAY_NAME", "PANE", "LAST_SEEN"}
	rows := make([][]string, 0, len(peers))
	for _, p := range peers {
		rows = append(rows, []string{
			peerDisplayLabel(p),
			cliview.DashIfEmpty(p.Slug),
			cliview.DashIfEmpty(p.Agent),
			cliview.DashIfEmpty(p.DisplayName),
			cliview.DashIfEmpty(p.PaneID),
			cliview.DashIfEmpty(p.LastSeen),
		})
	}
	writeMsgTable(out, headers, rows, make([]bool, len(peers)), lg.Colors())
}

// writeMsgTable renders the shared header/separator/rows layout via the
// cliview table primitives. dims[i] dims row i (read messages in --all views).
func writeMsgTable(out io.Writer, headers []string, rows [][]string, dims []bool, colors log.Palette) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, col := range row {
			widths[i] = max(widths[i], len(col))
		}
	}
	fmt.Fprintln(out, cliview.TableLine(headers, widths))
	separators := make([]string, len(headers))
	for i := range headers {
		separators[i] = strings.Repeat("-", widths[i])
	}
	fmt.Fprintln(out, cliview.TableLine(separators, widths))
	for i, row := range rows {
		line := cliview.TableLine(row, widths)
		if dims[i] {
			line = cliview.ColorWrap(colors.Dim, colors.Reset, line)
		}
		fmt.Fprintln(out, line)
	}
}

// msgTableBody flattens a message body to one table cell.
func msgTableBody(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
