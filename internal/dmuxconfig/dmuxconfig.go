// Package dmuxconfig handles dmux.config.json: read panes, look up the agent
// for the caller's tmux pane, find existing fanout-tagged panes, and write
// updates to a single pane's displayName without losing unknown fields.
//
// The "preserve unknown fields" property is non-negotiable. dmux's pane object
// has many fields fanout doesn't know about; round-tripping through a typed
// struct would silently drop them. So each pane is held as json.RawMessage and
// re-decoded on demand for the few fields fanout actually needs.
package dmuxconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/atomicfs"
)

type Config struct {
	path  string
	root  rawRoot
	panes []json.RawMessage
}

type rawRoot map[string]json.RawMessage

// Load reads the dmux.config.json file. Missing or malformed input returns an
// error that should be surfaced via die().
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dmux config %s: %w", path, err)
	}
	root := rawRoot{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse dmux config %s: %w", path, err)
	}
	cfg := &Config{path: path, root: root}
	if pr, ok := root["panes"]; ok {
		var panes []json.RawMessage
		if err := json.Unmarshal(pr, &panes); err != nil {
			return nil, fmt.Errorf("parse dmux panes: %w", err)
		}
		cfg.panes = panes
	}
	return cfg, nil
}

// PanesLen returns the number of pane objects currently in the config.
func (c *Config) PanesLen() int { return len(c.panes) }

// PaneField extracts a single string field from pane index i, or "" if absent.
func (c *Config) PaneField(i int, field string) string {
	if i < 0 || i >= len(c.panes) {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(c.panes[i], &m); err != nil {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}

// AgentForPane returns the .agent of the pane whose paneId == tmuxPaneID, or
// "" if no such pane.
func (c *Config) AgentForPane(tmuxPaneID string) string {
	for i := range c.panes {
		var m map[string]any
		if err := json.Unmarshal(c.panes[i], &m); err != nil {
			continue
		}
		if id, _ := m["paneId"].(string); id == tmuxPaneID {
			if a, ok := m["agent"].(string); ok {
				return a
			}
		}
	}
	return ""
}

var fanoutPrefixRE = regexp.MustCompile(`^\[fanout #([0-9]+)\]`)

// FannedNumbers returns the set of issue numbers prefixed `[fanout #N]` in
// any pane's prompt.
func (c *Config) FannedNumbers() map[int]bool {
	out := map[int]bool{}
	for i := range c.panes {
		var m map[string]any
		if err := json.Unmarshal(c.panes[i], &m); err != nil {
			continue
		}
		prompt, _ := m["prompt"].(string)
		if matches := fanoutPrefixRE.FindStringSubmatch(prompt); len(matches) == 2 {
			n, err := strconv.Atoi(matches[1])
			if err == nil {
				out[n] = true
			}
		}
	}
	return out
}

// FindPaneByFanoutTag returns slug + worktreePath for the pane whose prompt
// starts with `[fanout #<num>] `, or "", "" if no match.
func (c *Config) FindPaneByFanoutTag(num int) (slug, worktreePath string) {
	prefix := fmt.Sprintf("[fanout #%d]", num)
	for i := range c.panes {
		var m map[string]any
		if err := json.Unmarshal(c.panes[i], &m); err != nil {
			continue
		}
		p, _ := m["prompt"].(string)
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		slug, _ = m["slug"].(string)
		worktreePath, _ = m["worktreePath"].(string)
		return
	}
	return
}

// SetDisplayNameByFanoutTag rereads the file (so we don't trample dmux's
// concurrent saves), updates panes[].displayName for the pane whose prompt
// matches `[fanout #<num>] `, and atomically writes it back.
func SetDisplayNameByFanoutTag(path string, num int, displayName string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("[fanout #%d]", num)
	updated := false
	for i := range cfg.panes {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cfg.panes[i], &m); err != nil {
			continue
		}
		var prompt string
		if pr, ok := m["prompt"]; ok {
			_ = json.Unmarshal(pr, &prompt)
		}
		if !strings.HasPrefix(prompt, prefix) {
			continue
		}
		dn, _ := json.Marshal(displayName)
		m["displayName"] = dn
		// Re-marshal preserving deterministic field ordering.
		out, err := marshalSortedRaw(m)
		if err != nil {
			return err
		}
		cfg.panes[i] = out
		updated = true
	}
	if !updated {
		return fmt.Errorf("no pane found for [fanout #%d]", num)
	}

	// Re-pack panes, then root.
	pj, err := json.Marshal(cfg.panes)
	if err != nil {
		return err
	}
	cfg.root["panes"] = pj
	return atomicWriteJSON(path, cfg.root)
}

// marshalSortedRaw re-encodes a map keeping keys in alphabetical order so
// downstream bytes are deterministic between runs.
func marshalSortedRaw(m map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(m[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// atomicWriteJSON writes root to path atomically, indented to two spaces
// (dmux itself round-trips with two-space indentation).
func atomicWriteJSON(path string, root rawRoot) error {
	raw, err := marshalSortedRaw(root)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	return atomicfs.WriteFile(path, pretty.Bytes(), 0o644)
}
