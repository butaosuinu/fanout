package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseChannelsDefaultDedupeAndNone(t *testing.T) {
	got, err := ParseChannels("")
	if err != nil {
		t.Fatalf("ParseChannels default: %v", err)
	}
	if strings.Join(got, ",") != "bell" {
		t.Fatalf("default channels = %#v, want bell", got)
	}

	got, err = ParseChannels("bell, tmux bell")
	if err != nil {
		t.Fatalf("ParseChannels dedupe: %v", err)
	}
	if strings.Join(got, ",") != "bell,tmux" {
		t.Fatalf("deduped channels = %#v, want bell,tmux", got)
	}

	got, err = ParseChannels("none")
	if err != nil {
		t.Fatalf("ParseChannels none: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("none channels = %#v, want empty", got)
	}
}

func TestParseChannelsRejectsUnknown(t *testing.T) {
	_, err := ParseChannels("bell,email")
	if err == nil || !strings.Contains(err.Error(), "unknown notification channel") {
		t.Fatalf("ParseChannels error = %v, want unknown channel", err)
	}
}

func TestNotifierBellWritesOneBellPerEvent(t *testing.T) {
	var b strings.Builder
	n, err := New(Config{Channels: "bell", BellWriter: &b})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify([]Event{{Kind: EventMerged, IssueNum: 101}, {Kind: EventCIFailed, IssueNum: 102}})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := b.String(); got != "\a\a" {
		t.Fatalf("bell output = %q, want two bells", got)
	}
}

func TestNotifierPostsNtfyAndSlack(t *testing.T) {
	rt := &recordingRoundTripper{}

	n, err := New(Config{
		Channels:        "ntfy,slack",
		NtfyURL:         "https://ntfy.example/topic",
		SlackWebhookURL: "https://hooks.example/slack",
		HTTPClient:      &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify([]Event{{Kind: EventMerged, Parent: "100", IssueNum: 101, Title: "done", PRNumber: 901}})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(rt.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(rt.requests))
	}
	if got := rt.requests[0].url; got != "https://ntfy.example/topic" {
		t.Fatalf("ntfy URL = %q", got)
	}
	if rt.requests[0].method != http.MethodPost || !strings.Contains(rt.requests[0].body, "#101 done merged") {
		t.Fatalf("ntfy request = %#v, want POST merged message", rt.requests[0])
	}
	if got := rt.requests[1].url; got != "https://hooks.example/slack" {
		t.Fatalf("slack URL = %q", got)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(rt.requests[1].body), &payload); err != nil {
		t.Fatalf("decode slack payload: %v", err)
	}
	if rt.requests[1].method != http.MethodPost || !strings.Contains(payload["text"], "#101 done merged") {
		t.Fatalf("slack request = %#v payload=%#v, want POST merged message", rt.requests[1], payload)
	}
}

func TestNotifierRequiresOptInURLs(t *testing.T) {
	if _, err := New(Config{Channels: "ntfy"}); err == nil {
		t.Fatal("New(ntfy without URL) returned nil error")
	}
	if _, err := New(Config{Channels: "slack"}); err == nil {
		t.Fatal("New(slack without URL) returned nil error")
	}
}

type recordingRoundTripper struct {
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	url    string
	body   string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var body strings.Builder
	_, _ = io.Copy(&body, req.Body)
	r.requests = append(r.requests, recordedRequest{
		method: req.Method,
		url:    req.URL.String(),
		body:   body.String(),
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}
