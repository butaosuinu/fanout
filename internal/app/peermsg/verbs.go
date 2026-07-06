package peermsg

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// openMsgDB resolves the team DB path and opens it with the schema ensured.
// FANOUT_DB_PATH bypasses git entirely so orchestrators outside a checkout
// can still join; otherwise the owning project root names the DB.
func openMsgDB(verb, parent string, lg *log.Logger) (*sql.DB, exitcode.Code) {
	root := ""
	if os.Getenv(team.DBPathEnv) == "" {
		var err error
		root, err = team.OwnerProjectRoot()
		if err != nil {
			lg.Err("msg %s: %v; set %s or run inside the repository", verb, err, team.DBPathEnv)
			return nil, exitcode.Invocation
		}
	}
	path := team.DBPath(root, parent)
	db, err := team.Open(path)
	if err != nil {
		lg.Err("msg %s: %v", verb, err)
		return nil, exitcode.Backend
	}
	if err := team.EnsureSchema(db); err != nil {
		_ = db.Close()
		lg.Err("msg %s: %v", verb, err)
		return nil, exitcode.Backend
	}
	return db, exitcode.OK
}

func runMsgVerb(req *Request, store *msgstore.Store, self int, parent string, pane msgstore.Peer, lg *log.Logger) exitcode.Code {
	now := team.Now()
	switch req.Verb {
	case "peers":
		return runMsgPeers(req, store, parent, lg)
	case "inbox":
		msgs, marked, err := store.Inbox(self, req.All, req.MarkRead, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		return writeMsgMessages(req, store, self, parent, msgs, marked, lg)
	case "board":
		msgs, err := store.Board(self, req.All)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		return writeMsgMessages(req, store, self, parent, msgs, nil, lg)
	case "send":
		msg, err := store.Send(self, req.To, req.Kind, req.Body, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		return writeMsgResult(req, msgSendView(msg, parent, pane.TaskID, req.ToRaw), fmt.Sprintf("sent #%d to %s", msg.ID, recipientLabel(req, parent)), lg)
	case "post":
		msg, err := store.Post(self, req.Kind, req.Body, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		return writeMsgResult(req, msgSendView(msg, parent, pane.TaskID, ""), fmt.Sprintf("posted #%d to the board", msg.ID), lg)
	case "mark-read":
		return runMsgMarkRead(req, store, self, pane.TaskID, now, lg)
	case "register":
		peer, err := store.Register(pane, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		return writeMsgResult(req, msgRegisterReport{Peer: peer}, fmt.Sprintf("registered peer %s", peerDisplayLabel(peer)), lg)
	}
	// Unreachable while the CLI verb table and this switch stay in sync; fail
	// loud instead of silently exiting so a half-added verb is caught
	// immediately.
	lg.Err("msg: internal error: verb %s parsed but not implemented", req.Verb)
	return exitcode.Invocation
}

func runMsgPeers(req *Request, store *msgstore.Store, parent string, lg *log.Logger) exitcode.Code {
	peers, err := store.Peers()
	if err != nil {
		return msgBackendErr(req.Verb, err, lg)
	}
	if req.JSON {
		return writeMsgJSON(msgPeersReport{Parent: parent, Peers: peers}, lg)
	}
	writeMsgPeersTable(peers, lg)
	return exitcode.OK
}

func runMsgMarkRead(req *Request, store *msgstore.Store, self int, selfTask, now string, lg *log.Logger) exitcode.Code {
	// selfTask is the reader's plan task id ("" for issue/Project parents), so
	// plan-mode --json surfaces it alongside the synthetic Self number.
	report := msgMarkReadReport{Self: self, SelfTask: selfTask}
	if req.All {
		marked, cursor, err := store.MarkReadAll(self, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		report.MarkedIDs = marked
		report.BoardCursor = &cursor
	} else {
		marked, err := store.MarkReadIDs(self, req.IDs, now)
		if err != nil {
			return msgBackendErr(req.Verb, err, lg)
		}
		report.MarkedIDs = marked
	}
	summary := fmt.Sprintf("marked %d message(s) read", len(report.MarkedIDs))
	if report.BoardCursor != nil {
		summary += fmt.Sprintf(", board cursor at %d", *report.BoardCursor)
	}
	return writeMsgResult(req, report, summary, lg)
}

func msgBackendErr(verb string, err error, lg *log.Logger) exitcode.Code {
	lg.Err("msg %s: %v", verb, err)
	return exitcode.Backend
}
