package msgstore

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/team"
)

const testNow = "2026-06-13T00:00:00Z"

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "team.db")
	db, err := team.Open(path)
	if err != nil {
		t.Fatalf("team.Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close db: %v", closeErr)
		}
	})
	if schemaErr := team.EnsureSchema(db); schemaErr != nil {
		t.Fatalf("team.EnsureSchema: %v", schemaErr)
	}
	s, err := New(db, "68")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func mustSend(t *testing.T, s *Store, from, to int, kind, body string) Message {
	t.Helper()
	m, err := s.Send(from, to, kind, body, testNow)
	if err != nil {
		t.Fatalf("Send(%d->%d, %q): %v", from, to, body, err)
	}
	return m
}

func mustPost(t *testing.T, s *Store, from int, kind, body string) Message {
	t.Helper()
	m, err := s.Post(from, kind, body, testNow)
	if err != nil {
		t.Fatalf("Post(%d, %q): %v", from, body, err)
	}
	return m
}

// seedStore loads the canonical fixture: two 1:1 messages to 70, one board
// post by 71, one board post by 70 (self), and one 1:1 to 71 (not ours).
func seedStore(t *testing.T, s *Store) {
	t.Helper()
	mustSend(t, s, 71, 70, "note", "first to 70")     // id 1
	mustSend(t, s, 71, 70, "blocker", "second to 70") // id 2
	mustPost(t, s, 71, "note", "board by 71")         // id 3
	mustPost(t, s, 70, "note", "board by 70")         // id 4
	mustSend(t, s, 70, 71, "note", "to 71, not ours") // id 5
}

func messageIDs(msgs []Message) []int64 {
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func TestSendAndPostAssignSequentialIDs(t *testing.T) {
	s := openTestStore(t)
	sent, err := s.Send(70, 71, "note", "hello", testNow)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	posted, err := s.Post(70, "note", "to the board", testNow)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if sent.ID != 1 || posted.ID != 2 {
		t.Errorf("ids = %d, %d, want 1, 2", sent.ID, posted.ID)
	}
	if sent.Board || posted.To != nil || !posted.Board {
		t.Errorf("board flags wrong: sent=%+v posted=%+v", sent, posted)
	}
}

func TestInboxUnreadUnion(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	msgs, marked, err := s.Inbox(70, false, false, testNow)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if marked != nil {
		t.Errorf("marked = %+v, want nil without markRead", marked)
	}
	// Unread for 70: 1:1 ids 1-2 plus board id 3. Own board post (4) and the
	// message to 71 (5) are excluded.
	if got, want := messageIDs(msgs), []int64{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("inbox ids = %v, want %v", got, want)
	}
}

func TestInboxAllIncludesReadAndOwnBoardPosts(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)
	if _, err := s.MarkReadIDs(70, []int64{1}, testNow); err != nil {
		t.Fatalf("MarkReadIDs: %v", err)
	}

	msgs, _, err := s.Inbox(70, true, false, testNow)
	if err != nil {
		t.Fatalf("Inbox --all: %v", err)
	}
	if got, want := messageIDs(msgs), []int64{1, 2, 3, 4}; !slices.Equal(got, want) {
		t.Errorf("inbox --all ids = %v, want %v", got, want)
	}
	if msgs[0].ReadAt == nil || *msgs[0].ReadAt != testNow {
		t.Errorf("id 1 read_at = %v, want %q", msgs[0].ReadAt, testNow)
	}
}

func TestInboxMarkReadDrainsAndIsScopedToDisplayedRows(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	msgs, marked, err := s.Inbox(70, false, true, testNow)
	if err != nil {
		t.Fatalf("Inbox --mark-read: %v", err)
	}
	if got, want := messageIDs(msgs), []int64{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("displayed ids = %v, want %v", got, want)
	}
	if got, want := marked.MessageIDs, []int64{1, 2}; !slices.Equal(got, want) {
		t.Errorf("marked ids = %v, want %v", got, want)
	}
	if marked.BoardCursor != 3 {
		t.Errorf("board cursor = %d, want 3", marked.BoardCursor)
	}

	again, _, err := s.Inbox(70, false, false, testNow)
	if err != nil {
		t.Fatalf("second Inbox: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second inbox = %v, want empty", messageIDs(again))
	}

	// id 4 (own board post, beyond the cursor only via --all) and id 5
	// (addressed to 71) must be untouched.
	other, _, err := s.Inbox(71, false, false, testNow)
	if err != nil {
		t.Fatalf("Inbox for 71: %v", err)
	}
	if got, want := messageIDs(other), []int64{4, 5}; !slices.Equal(got, want) {
		t.Errorf("inbox for 71 = %v, want %v", got, want)
	}
}

func TestBoardCursorAndAll(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	unread, err := s.Board(70, false)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if got, want := messageIDs(unread), []int64{3}; !slices.Equal(got, want) {
		t.Errorf("board unread = %v, want %v (own post excluded)", got, want)
	}

	all, err := s.Board(70, true)
	if err != nil {
		t.Fatalf("Board --all: %v", err)
	}
	if got, want := messageIDs(all), []int64{3, 4}; !slices.Equal(got, want) {
		t.Errorf("board --all = %v, want %v", got, want)
	}

	// Board never advances the cursor.
	again, err := s.Board(70, false)
	if err != nil {
		t.Fatalf("second Board: %v", err)
	}
	if got, want := messageIDs(again), []int64{3}; !slices.Equal(got, want) {
		t.Errorf("board after read = %v, want %v", got, want)
	}
}

func TestMarkReadIDsSkipsForeignBoardAndMissing(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	// 1 is ours; 3 is a board post, 5 is addressed to 71, 99 does not exist.
	marked, err := s.MarkReadIDs(70, []int64{1, 3, 5, 99}, testNow)
	if err != nil {
		t.Fatalf("MarkReadIDs: %v", err)
	}
	if got, want := marked, []int64{1}; !slices.Equal(got, want) {
		t.Errorf("marked = %v, want %v", got, want)
	}

	// Idempotent: a second run finds nothing left to mark.
	marked, err = s.MarkReadIDs(70, []int64{1}, testNow)
	if err != nil {
		t.Fatalf("second MarkReadIDs: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("second marked = %v, want empty", marked)
	}
}

func TestMarkReadAllAdvancesCursorAndNeverRegresses(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	marked, cursor, err := s.MarkReadAll(70, testNow)
	if err != nil {
		t.Fatalf("MarkReadAll: %v", err)
	}
	if got, want := marked, []int64{1, 2}; !slices.Equal(got, want) {
		t.Errorf("marked = %v, want %v", got, want)
	}
	if cursor != 4 {
		t.Errorf("cursor = %d, want 4 (max board id)", cursor)
	}

	// A later partial inbox --mark-read sees nothing unread and must not
	// rewind the cursor below 4.
	_, res, err := s.Inbox(70, false, true, testNow)
	if err != nil {
		t.Fatalf("Inbox --mark-read after MarkReadAll: %v", err)
	}
	if res.BoardCursor != 4 {
		t.Errorf("cursor after empty mark = %d, want 4", res.BoardCursor)
	}
}

func TestNewRejectsForeignParent(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)

	// The v1 schema keys peers/board_cursors by issue alone, so one DB file
	// must serve one parent; opening it for another parent must fail loudly.
	if _, err := New(s.db, "99"); err == nil || !strings.Contains(err.Error(), "one team DB serves one parent") {
		t.Fatalf("New for foreign parent = %v, want single-parent rejection", err)
	}
	if _, err := New(s.db, "68"); err != nil {
		t.Fatalf("New for the resident parent: %v", err)
	}
}

func TestNewSeedsLegacyOwnerFromMessages(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)
	// Simulate a DB created before msg_db_owner existed.
	if _, err := s.db.Exec("DROP TABLE msg_db_owner"); err != nil {
		t.Fatalf("drop owner table: %v", err)
	}

	// A wrong first opener must not hijack ownership of the legacy DB: the
	// claim seeds from the resident parent found in messages, so the open is
	// rejected AND the recorded owner stays the resident.
	if _, err := New(s.db, "99"); err == nil || !strings.Contains(err.Error(), "owned by parent 68") {
		t.Fatalf("New(99) on a legacy DB = %v, want rejection naming resident 68", err)
	}
	if _, err := New(s.db, "68"); err != nil {
		t.Fatalf("New(68) after the failed hijack: %v", err)
	}
}

func TestNewOwnershipSurvivesRegisterOnlyDB(t *testing.T) {
	s := openTestStore(t)
	// Register a peer but write no message: the messages table alone cannot
	// reveal the resident parent, so ownership must come from the claim.
	if _, err := s.Register(Peer{Issue: 70}, testNow); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := New(s.db, "99"); err == nil || !strings.Contains(err.Error(), "owned by parent 68") {
		t.Fatalf("New on a register-only DB for a foreign parent = %v, want ownership rejection", err)
	}
	if _, err := New(s.db, "68"); err != nil {
		t.Fatalf("reopening for the owner: %v", err)
	}
}

// Defense in depth: even if a mixed DB exists (constructed here by bypassing
// New), every messages statement stays parent-scoped so cross-parent reads
// and mark-read --id (user-supplied ids) cannot touch another team's rows.
func TestParentScopesMessages(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)
	other := &Store{db: s.db, parent: "99"}

	msgs, _, err := other.Inbox(70, false, false, testNow)
	if err != nil {
		t.Fatalf("Inbox other parent: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("other parent inbox = %v, want empty", messageIDs(msgs))
	}

	marked, err := other.MarkReadIDs(70, []int64{1, 2}, testNow)
	if err != nil {
		t.Fatalf("MarkReadIDs other parent: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("other parent marked = %v, want empty", marked)
	}
	still, _, err := s.Inbox(70, false, false, testNow)
	if err != nil {
		t.Fatalf("Inbox after cross-parent attempt: %v", err)
	}
	if got, want := messageIDs(still), []int64{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("inbox after cross-parent mark = %v, want %v (untouched)", got, want)
	}
}

func TestMarkReadIDsEmptyListIsANoOp(t *testing.T) {
	s := openTestStore(t)
	seedStore(t, s)
	marked, err := s.MarkReadIDs(70, nil, testNow)
	if err != nil {
		t.Fatalf("MarkReadIDs(nil): %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("marked = %v, want empty", marked)
	}
}

func TestRegisterUpsertPreservesJoinedAt(t *testing.T) {
	s := openTestStore(t)
	first, err := s.Register(Peer{
		Issue: 70, PaneID: "%1", Slug: "msg-cli-surface-70", Agent: "claude", DisplayName: "msg cli",
	}, "2026-06-13T00:00:00Z")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if first.JoinedAt != "2026-06-13T00:00:00Z" || first.LastSeen != "2026-06-13T00:00:00Z" {
		t.Errorf("first register = %+v", first)
	}

	second, err := s.Register(Peer{Issue: 70, PaneID: "%2", Agent: "codex"}, "2026-06-13T01:00:00Z")
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if second.JoinedAt != "2026-06-13T00:00:00Z" {
		t.Errorf("joined_at = %q, want preserved %q", second.JoinedAt, "2026-06-13T00:00:00Z")
	}
	if second.LastSeen != "2026-06-13T01:00:00Z" || second.PaneID != "%2" || second.Agent != "codex" {
		t.Errorf("second register = %+v", second)
	}
	if second.Slug != "" {
		t.Errorf("slug = %q, want overwritten to empty", second.Slug)
	}

	peers, err := s.Peers()
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) != 1 || peers[0].Issue != 70 {
		t.Errorf("peers = %+v, want one row for 70", peers)
	}
}

func TestPeersOrderedByIssue(t *testing.T) {
	s := openTestStore(t)
	for _, issue := range []int{71, 69, 70} {
		if _, err := s.Register(Peer{Issue: issue}, testNow); err != nil {
			t.Fatalf("Register(%d): %v", issue, err)
		}
	}
	peers, err := s.Peers()
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	got := make([]int, len(peers))
	for i, p := range peers {
		got[i] = p.Issue
	}
	if want := []int{69, 70, 71}; !slices.Equal(got, want) {
		t.Errorf("peer order = %v, want %v", got, want)
	}
}

// TestPlanTaskPeerAndMessagingRoundTrip exercises the issue-less plan path:
// peers carry a synthetic negative number plus a task id, and 1:1 messaging
// round-trips between two such peers through the unchanged int-keyed schema.
func TestPlanTaskPeerAndMessagingRoundTrip(t *testing.T) {
	const parent = "plan:launch-plan"
	path := filepath.Join(t.TempDir(), "team.db")
	db, err := team.Open(path)
	if err != nil {
		t.Fatalf("team.Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close db: %v", closeErr)
		}
	})
	if schemaErr := team.EnsureSchema(db); schemaErr != nil {
		t.Fatalf("team.EnsureSchema: %v", schemaErr)
	}
	s, err := New(db, parent)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	apiNum := team.TaskPeerNum(parent, "api-client")
	dbNum := team.TaskPeerNum(parent, "db-layer")
	if apiNum >= 0 || dbNum >= 0 {
		t.Fatalf("synthetic numbers must be negative, got api=%d db=%d", apiNum, dbNum)
	}

	stored, err := s.Register(Peer{Issue: apiNum, TaskID: "api-client", Slug: "launch-plan-api-client"}, testNow)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if stored.Issue != apiNum || stored.TaskID != "api-client" {
		t.Fatalf("Register stored = %+v, want issue %d task api-client", stored, apiNum)
	}

	if _, sendErr := s.Send(dbNum, apiNum, "note", "schema ready", testNow); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	msgs, _, err := s.Inbox(apiNum, false, false, testNow)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(msgs) != 1 || msgs[0].From != dbNum || msgs[0].To == nil || *msgs[0].To != apiNum {
		t.Fatalf("Inbox(api) = %+v, want one db-layer->api-client message", msgs)
	}

	peers, err := s.Peers()
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) != 1 || peers[0].TaskID != "api-client" {
		t.Fatalf("Peers = %+v, want one peer with task id api-client", peers)
	}
}
