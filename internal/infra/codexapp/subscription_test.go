package codexapp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"testing"
	"time"
)

// This peer models Codex 0.155.1's connection-scoped subscription: discovering
// the remote TUI's thread or sending turn/start does not subscribe an observer.
func TestRemoteTUIThreadSubscriptionDeliversTeamCompletion(t *testing.T) {
	for _, mode := range []string{"fresh-active", "fresh-completed", "resume", "reconnected-B2"} {
		t.Run(mode, func(t *testing.T) {
			fresh := mode == "fresh-active" || mode == "fresh-completed"
			c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
				return serveSubscriptionPeer(peer, fresh, mode == "fresh-completed")
			})
			session := &codexRemoteTUISession{
				client: c, thread: codexThreadInfo{ID: "thread-1"}, freshThread: fresh,
				tuiDone: make(chan error), server: &appServer{done: make(chan struct{})},
			}
			if err := session.observeThread(codexRemoteTUIConfig{}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0)
			fetches := 0
			bridge := newTestTeamBridge(newFakeTeamAppClient(), &now, func() ([]InboundMessage, error) {
				fetches++
				return []InboundMessage{{Line: "[fanout msg #1] test marker"}}, nil
			})
			bridge.client = c
			done := make(chan struct{})
			defer close(done)
			if err := bridge.start(session, "transport test", done); err != nil {
				t.Fatal(err)
			}
			for batch := range 3 {
				for bridge.lastTurnCompleted.IsZero() || bridge.activeTurnID != "" || bridge.pendingStart != nil {
					bridge.poll()
					if fetches != batch {
						t.Fatalf("fetched before turn completion: %d", fetches)
					}
					result := <-bridge.received
					if result.err != nil {
						t.Fatal(result.err)
					}
					if result := bridge.handleMessage(result.msg); result.err != nil {
						t.Fatal(result.err)
					}
				}
				bridge.poll()
				if fetches != batch {
					t.Fatal("fetched during grace")
				}
				if batch < 2 {
					now = now.Add(bridge.idleGrace)
					bridge.poll()
					bridge.poll()
					if fetches != batch+1 {
						t.Fatalf("fetches=%d, want one per eligible poll", fetches)
					}
				}
			}
		})
	}
}

func serveSubscriptionPeer(peer *websocketJSONConn, fresh, completedBeforeSubscribe bool) error {
	if fresh {
		if err := peer.Send(appServerMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"thread-1"}}`)}); err != nil {
			return err
		}
	}
	subscribed, started, injected := false, false, 0
	for injected < 2 {
		msg, err := peer.Receive()
		if err != nil {
			return err
		}
		switch msg.Method {
		case "thread/resume":
			if fresh && !started {
				return fmt.Errorf("no rollout before initial turn acceptance")
			}
			if string(msg.Params) != `{"threadId":"thread-1"}` {
				return fmt.Errorf("resume overrides settings: %s", msg.Params)
			}
			subscribed = true
			status, turnStatus := "idle", "completed"
			if fresh && !completedBeforeSubscribe {
				status, turnStatus = "active", "inProgress"
			}
			snapshot := fmt.Sprintf(`{"thread":{"id":"thread-1","status":{"type":%q},"turns":[{"id":"initial","status":%q}]}}`, status, turnStatus)
			if err := peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(snapshot)}); err != nil {
				return err
			}
			if fresh && !completedBeforeSubscribe {
				if err := peer.Send(teamTurnCompletedMessage("thread-1", "initial", "completed")); err != nil {
					return err
				}
			}
		case "turn/start":
			started = true
			turnID := "initial"
			if !messageIDMatches(msg.ID, teamInitialTurnRequestID) {
				injected++
				turnID = fmt.Sprintf("injected-%d", injected)
			}
			if err := peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(fmt.Sprintf(`{"turn":{"id":%q,"status":"inProgress"}}`, turnID))}); err != nil {
				return err
			}
			if subscribed {
				if err := peer.Send(teamTurnCompletedMessage("thread-1", turnID, "completed")); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected method %s", msg.Method)
		}
	}
	return nil
}

func TestPlanStartupRetainsCompletionBeforeResponse(t *testing.T) {
	c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
		msg, err := peer.Receive()
		if err != nil {
			return err
		}
		for _, notification := range []appServerMessage{
			teamTurnStartedMessage("thread-1", "initial"),
			teamTurnCompletedMessage("other-thread", "other", "completed"),
			teamTurnCompletedMessage("thread-1", "old-turn", "completed"),
			teamTurnCompletedMessage("thread-1", "initial", "completed"),
		} {
			if err := peer.Send(notification); err != nil {
				return err
			}
		}
		return peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(`{"turn":{"id":"initial","status":"inProgress"}}`)})
	})
	turn, err := startCodexPlanTurn(c, codexThreadInfo{ID: "thread-1"}, "/repo", "transport test")
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	if err := drainCodexAppServerDuringStartup(c, recordedStates(&states), "thread-1", turn.TurnID); err != nil {
		t.Fatal(err)
	}
	tracker := newCodexPlanScreenTracker(nil, recordedStates(&states), true)
	tracker.initialTurnCompleted()
	if !slices.Equal(states, []string{"working", "idle"}) {
		t.Fatalf("Plan states without capture = %v", states)
	}
}

func TestPlanStartupRecoversCompletionFromSubscriptionSnapshot(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
				msg, err := peer.Receive()
				if err != nil {
					return err
				}
				snapshot := fmt.Sprintf(`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"initial","status":%q}]}}`, status)
				if err := peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(snapshot)}); err != nil {
					return err
				}
				for _, event := range []appServerMessage{teamTurnStartedMessage("thread-1", "later"), teamTurnCompletedMessage("thread-1", "later", "completed")} {
					if err := peer.Send(event); err != nil {
						return err
					}
				}
				return nil
			})
			if err := subscribeCodexRemoteTUIThread(c, "thread-1"); err != nil {
				t.Fatal(err)
			}
			var states []string
			err := drainCodexAppServerDuringStartup(c, recordedStates(&states), "thread-1", "initial")
			if (err == nil) != (status == "completed") {
				t.Fatalf("initial %s: %v", status, err)
			}
			for range 2 {
				msg, err := c.receive()
				if err != nil {
					t.Fatal(err)
				}
				reportCodexPlanAgentState(recordedStates(&states), codexTurnNotificationAgentState(msg))
			}
			if !slices.Equal(states[len(states)-2:], []string{"working", "idle"}) {
				t.Fatalf("subsequent Plan turn states = %v", states)
			}
		})
	}
}

func TestSubscriptionSnapshotFollowsEarlierNotifications(t *testing.T) {
	c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
		msg, err := peer.Receive()
		if err != nil {
			return err
		}
		if err := peer.Send(teamTurnCompletedMessage("thread-1", "old", "completed")); err != nil {
			return err
		}
		return peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active"},"turns":[{"id":"current","status":"inProgress"}]}}`)})
	})
	if err := subscribeCodexRemoteTUIThread(c, "thread-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	bridge := newTestTeamBridge(newFakeTeamAppClient(), &now, func() ([]InboundMessage, error) {
		t.Fatal("fetched during resumed active turn")
		return nil, nil
	})
	for range 2 {
		msg, err := c.receive()
		if err != nil {
			t.Fatal(err)
		}
		bridge.handleMessage(msg)
	}
	now = now.Add(time.Hour)
	bridge.poll()
	if bridge.activeTurnID != "current" {
		t.Fatalf("active turn after handoff = %q", bridge.activeTurnID)
	}
}

func TestSubscriptionRetriesOnlyExplicitPersistenceRejections(t *testing.T) {
	c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
		for _, rejection := range []string{
			`{"code":-32600,"message":"no rollout found for thread id thread-1"}`,
			`{"code":-32603,"message":"failed to read thread: rollout at /test/session.jsonl is empty"}`,
			"",
		} {
			msg, err := peer.Receive()
			if err != nil {
				return err
			}
			if msg.Method != "thread/resume" {
				return fmt.Errorf("retried mutation %s", msg.Method)
			}
			response := appServerMessage{ID: msg.ID, Error: json.RawMessage(rejection)}
			if rejection == "" {
				response.Result = json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`)
			}
			if err := peer.Send(response); err != nil {
				return err
			}
		}
		return nil
	})
	if err := subscribeCodexRemoteTUIThread(c, "thread-1"); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{nil, io.EOF, io.ErrClosedPipe, fmt.Errorf("request failed: unsupported method (code -32601)")} {
		if codexThreadPersistencePending(err) {
			t.Fatalf("would retry ambiguous/permanent error: %v", err)
		}
	}
}

func TestSubscriptionLeavesApprovalForRemoteTUI(t *testing.T) {
	c := newCodexPipeClient(t, func(peer *websocketJSONConn) error {
		msg, err := peer.Receive()
		if err != nil {
			return err
		}
		approval := appServerMessage{ID: json.RawMessage(`61`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread-1"}`)}
		if err := peer.Send(approval); err != nil {
			return err
		}
		return peer.Send(appServerMessage{ID: msg.ID, Result: json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`)})
	})
	if err := subscribeCodexRemoteTUIThread(c, "thread-1"); err != nil {
		t.Fatal(err)
	}
	msg, err := c.receive()
	if err != nil || !isServerRequest(msg) || string(msg.ID) != "61" {
		t.Fatalf("approval lost during subscription: %+v, %v", msg, err)
	}
}

func newCodexPipeClient(t *testing.T, serve func(*websocketJSONConn) error) *client {
	t.Helper()
	conn, peer := net.Pipe()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serve(&websocketJSONConn{conn: peer, br: bufio.NewReader(peer)})
		// Keep the transport alive until the test has consumed the transcript.
		_, _ = io.Copy(io.Discard, peer)
	}()
	c := &client{conn: &websocketJSONConn{conn: conn, br: bufio.NewReader(conn)}}
	t.Cleanup(func() {
		c.Close()
		_ = peer.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return c
}
