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
	cfg := &Config{root: root}
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

var fanoutPrefixRE = regexp.MustCompile(`^\[fanout #([0-9]+)( of #([^\]]+))?\]`)

// ProjectRoot returns dmux's project root when present. dmux versions have
// used both projectRoot and project_root.
func (c *Config) ProjectRoot() string {
	for _, key := range []string{"projectRoot", "project_root"} {
		raw, ok := c.root[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return ""
}

// FannedNumbersForParent returns issue numbers from fanout-tagged prompts,
// optionally filtering to one parent. Legacy prompts without "of #parent" are
// counted only when the caller's claim set contains the child number.
func (c *Config) FannedNumbersForParent(parent string, legacyClaim map[int]bool) map[int]bool {
	out := map[int]bool{}
	for i := range c.panes {
		var m map[string]any
		if err := json.Unmarshal(c.panes[i], &m); err != nil {
			continue
		}
		prompt, _ := m["prompt"].(string)
		matches := fanoutPrefixRE.FindStringSubmatch(prompt)
		if len(matches) == 0 {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		paneParent := matches[3]
		if parent != "" && !parentMatches(paneParent, parent, n, legacyClaim) {
			continue
		}
		out[n] = true
	}
	return out
}

// FindPaneByFanoutTag returns slug + worktreePath for the pane whose prompt
// starts with `[fanout #<num> of #<parent>]`, or "", "" if no match.
func (c *Config) FindPaneByFanoutTag(num int, parent string) (slug, worktreePath string) {
	prefix := fmt.Sprintf("[fanout #%d of #%s]", num, parent)
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

func parentMatches(paneParent, filterParent string, num int, legacyClaim map[int]bool) bool {
	if paneParent == "" {
		return legacyClaim != nil && legacyClaim[num]
	}
	pn, perr := strconv.Atoi(paneParent)
	fn, ferr := strconv.Atoi(filterParent)
	if perr == nil && ferr == nil {
		return pn == fn
	}
	return paneParent == filterParent
}

// LegacyMigrationCount counts legacy [fanout #N] prompts that are strongly
// owned by this parent and should be retagged with "of #parent".
func (c *Config) LegacyMigrationCount(strong map[int]bool) int {
	count := 0
	for i := range c.panes {
		var m map[string]any
		if err := json.Unmarshal(c.panes[i], &m); err != nil {
			continue
		}
		prompt, _ := m["prompt"].(string)
		matches := fanoutPrefixRE.FindStringSubmatch(prompt)
		if len(matches) == 0 || matches[3] != "" {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err == nil && strong[n] {
			count++
		}
	}
	return count
}

// MigrateLegacyPaneTags rewrites legacy [fanout #N] prompt prefixes for the
// strong-ownership set to include this run's parent annotation.
func MigrateLegacyPaneTags(path, parent string, strong map[int]bool) (int, error) {
	cfg, err := Load(path)
	if err != nil {
		return 0, err
	}
	count := cfg.LegacyMigrationCount(strong)
	if count == 0 {
		return 0, nil
	}
	for i := range cfg.panes {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cfg.panes[i], &m); err != nil {
			continue
		}
		var prompt string
		if raw, ok := m["prompt"]; ok {
			_ = json.Unmarshal(raw, &prompt)
		}
		matches := fanoutPrefixRE.FindStringSubmatch(prompt)
		if len(matches) == 0 || matches[3] != "" {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err != nil || !strong[n] {
			continue
		}
		rest := strings.TrimPrefix(prompt, matches[0])
		repl := fmt.Sprintf("[fanout #%d of #%s]%s", n, parent, rest)
		raw, _ := json.Marshal(repl)
		m["prompt"] = raw
		out, err := marshalSortedRaw(m)
		if err != nil {
			return 0, err
		}
		cfg.panes[i] = out
	}
	pj, err := json.Marshal(cfg.panes)
	if err != nil {
		return 0, err
	}
	cfg.root["panes"] = pj
	return count, atomicWriteJSON(path, cfg.root)
}

// SetDisplayNameBySlug rereads the file (so we don't trample dmux's concurrent
// saves), updates panes[].displayName for the pane whose `slug` matches, and
// atomically writes it back.
//
// Targeting by slug (not by `[fanout #N]` prompt) matches the bash predecessor
// and stays correct when an external editor or dmux itself rewrites the prompt
// after pane creation; the slug is immutable once dmux generates it.
func SetDisplayNameBySlug(path, slug, displayName string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	updated := false
	for i := range cfg.panes {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cfg.panes[i], &m); err != nil {
			continue
		}
		var paneSlug string
		if s, ok := m["slug"]; ok {
			_ = json.Unmarshal(s, &paneSlug)
		}
		if paneSlug != slug {
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
		return fmt.Errorf("no pane found with slug %q", slug)
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
