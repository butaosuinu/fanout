// Package notify delivers fanout state-transition notifications.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

const (
	DefaultChannels = "bell"

	ChannelBell  = "bell"
	ChannelTmux  = "tmux"
	ChannelNtfy  = "ntfy"
	ChannelSlack = "slack"
	ChannelNone  = "none"
)

type EventKind string

const (
	EventMerged       EventKind = "merged"
	EventCIFailed     EventKind = "ci-failed"
	EventWaiting      EventKind = "waiting"
	EventAgentPlan    EventKind = "agent-plan"
	EventAgentBlocked EventKind = "agent-blocked"
	EventAgentDone    EventKind = "agent-done"
)

// Event describes one child state transition detected between snapshots.
type Event struct {
	Kind       EventKind
	Parent     string
	IssueNum   int
	TaskID     string
	Title      string
	PRNumber   int
	CIStatus   string
	Blockers   string
	IssueLink  string
	PaneID     string
	SourceKey  string
	AgentState string
}

// Message renders the event as one compact operator-facing line.
func (e Event) Message() string {
	subject := e.subject()
	parent := e.parentSuffix()
	switch e.Kind {
	case EventMerged:
		return fmt.Sprintf("fanout: %s merged%s%s", subject, prSuffix(e.PRNumber), parent)
	case EventCIFailed:
		return fmt.Sprintf("fanout: %s CI failed%s%s", subject, prSuffix(e.PRNumber), parent)
	case EventWaiting:
		blockers := ""
		if strings.TrimSpace(e.Blockers) != "" && strings.TrimSpace(e.Blockers) != "-" {
			blockers = " on " + strings.TrimSpace(e.Blockers)
		}
		return fmt.Sprintf("fanout: %s waiting%s%s", subject, blockers, parent)
	case EventAgentPlan:
		return fmt.Sprintf("fanout: %s plan ready%s", subject, parent)
	case EventAgentBlocked:
		return fmt.Sprintf("fanout: %s waiting for input%s", subject, parent)
	case EventAgentDone:
		return fmt.Sprintf("fanout: %s agent exited%s", subject, parent)
	default:
		return fmt.Sprintf("fanout: %s changed%s", subject, parent)
	}
}

func (e Event) subject() string {
	title := strings.TrimSpace(e.Title)
	switch {
	case e.IssueNum > 0:
		subject := fmt.Sprintf("#%d", e.IssueNum)
		if title != "" {
			subject += " " + title
		}
		return subject
	case strings.TrimSpace(e.TaskID) != "":
		task := strings.TrimSpace(e.TaskID)
		subject := "task " + task
		if title != "" && title != task {
			subject += " " + title
		}
		return subject
	case title != "":
		return title
	case strings.TrimSpace(e.PaneID) != "":
		return "pane " + strings.TrimSpace(e.PaneID)
	case strings.TrimSpace(e.SourceKey) != "":
		return "source " + strings.TrimSpace(e.SourceKey)
	case strings.TrimSpace(e.Parent) != "":
		return "session " + strings.TrimSpace(e.Parent)
	default:
		return "session"
	}
}

func (e Event) parentSuffix() string {
	if strings.TrimSpace(e.Parent) == "" {
		return ""
	}
	return " (parent " + strings.TrimSpace(e.Parent) + ")"
}

func prSuffix(num int) string {
	if num <= 0 {
		return ""
	}
	return fmt.Sprintf(" (PR #%d)", num)
}

// Config selects notification channels and the channel-specific delivery
// endpoints. Channels is a comma/space separated list of bell, tmux, ntfy,
// slack, or none. Empty means DefaultChannels.
type Config struct {
	Channels        string
	TmuxTarget      string
	NtfyURL         string
	SlackWebhookURL string
	BellWriter      io.Writer
	HTTPClient      *http.Client
}

// Notifier sends one notification per event to every configured channel.
type Notifier struct {
	sinks []sink
}

type sink interface {
	Notify(Event) error
}

type bellSink struct {
	w io.Writer
}

type tmuxSink struct {
	target string
}

type ntfySink struct {
	url    string
	client *http.Client
}

type slackSink struct {
	url    string
	client *http.Client
}

// New constructs a notifier from Config.
func New(cfg Config) (*Notifier, error) {
	channels, err := ParseChannels(cfg.Channels)
	if err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	sinks := make([]sink, 0, len(channels))
	for _, channel := range channels {
		switch channel {
		case ChannelBell:
			w := cfg.BellWriter
			if w == nil {
				w = io.Discard
			}
			sinks = append(sinks, bellSink{w: w})
		case ChannelTmux:
			sinks = append(sinks, tmuxSink{target: cfg.TmuxTarget})
		case ChannelNtfy:
			if strings.TrimSpace(cfg.NtfyURL) == "" {
				return nil, fmt.Errorf("ntfy notifications require FANOUT_NTFY_URL or ntfyURL")
			}
			sinks = append(sinks, ntfySink{url: strings.TrimSpace(cfg.NtfyURL), client: client})
		case ChannelSlack:
			if strings.TrimSpace(cfg.SlackWebhookURL) == "" {
				return nil, fmt.Errorf("slack notifications require FANOUT_SLACK_WEBHOOK_URL or slackWebhookURL")
			}
			sinks = append(sinks, slackSink{url: strings.TrimSpace(cfg.SlackWebhookURL), client: client})
		}
	}
	return &Notifier{sinks: sinks}, nil
}

// ParseChannels parses and validates a channel selector.
func ParseChannels(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultChannels
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := map[string]bool{}
	var out []string
	for _, part := range parts {
		ch := strings.ToLower(strings.TrimSpace(part))
		if ch == "" || seen[ch] {
			continue
		}
		switch ch {
		case ChannelNone:
			return nil, nil
		case ChannelBell, ChannelTmux, ChannelNtfy, ChannelSlack:
			seen[ch] = true
			out = append(out, ch)
		default:
			return nil, fmt.Errorf("unknown notification channel %q (expected bell,tmux,ntfy,slack,none)", part)
		}
	}
	return out, nil
}

// Notify delivers all events. It keeps trying every channel and returns joined
// errors for failed deliveries.
func (n *Notifier) Notify(events []Event) error {
	if n == nil || len(events) == 0 || len(n.sinks) == 0 {
		return nil
	}
	var notifyErr error
	for _, event := range events {
		for _, sink := range n.sinks {
			if err := sink.Notify(event); err != nil {
				notifyErr = errors.Join(notifyErr, err)
			}
		}
	}
	return notifyErr
}

func (s bellSink) Notify(Event) error {
	if s.w == nil {
		return nil
	}
	_, err := io.WriteString(s.w, "\a")
	return err
}

func (s tmuxSink) Notify(event Event) error {
	return tmuxrun.DisplayMessage(s.target, event.Message())
}

func (s ntfySink) Notify(event Event) error {
	req, err := http.NewRequest(http.MethodPost, s.url, strings.NewReader(event.Message()))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "fanout")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy post: %w", err)
	}
	// Fire-and-forget notification; only the status code matters, not the body.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy post: status %d", resp.StatusCode)
	}
	return nil
}

func (s slackSink) Notify(event Event) error {
	body, err := json.Marshal(map[string]string{"text": event.Message()})
	if err != nil {
		return err
	}
	resp, err := s.client.Post(s.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack post: %w", err)
	}
	// Fire-and-forget notification; only the status code matters, not the body.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack post: status %d", resp.StatusCode)
	}
	return nil
}
