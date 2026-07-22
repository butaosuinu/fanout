package herdrrun

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const minimumVersion = "0.7.5"

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func parseAdmittedVersion(output []byte) (string, error) {
	raw := strings.TrimSpace(string(output))
	version, ok := strings.CutPrefix(raw, "herdr ")
	if !ok || version == "" || strings.ContainsAny(version, " \t\r\n") {
		return "", fmt.Errorf("unsupported herdr CLI version %q (required: herdr stable >=%s)", raw, minimumVersion)
	}
	if err := validateAdmittedVersion(version); err != nil {
		return "", fmt.Errorf("unsupported herdr CLI version %q: %w", raw, err)
	}
	return version, nil
}

func validateAdmittedVersion(version string) error {
	got := stableVersionPattern.FindStringSubmatch(version)
	floor := stableVersionPattern.FindStringSubmatch(minimumVersion)
	if got == nil {
		return fmt.Errorf("required: stable >=%s", minimumVersion)
	}
	for i := 1; i <= 3; i++ {
		if len(got[i]) != len(floor[i]) {
			if len(got[i]) < len(floor[i]) {
				return fmt.Errorf("version %s is below floor %s", version, minimumVersion)
			}
			return nil
		}
		if got[i] < floor[i] {
			return fmt.Errorf("version %s is below floor %s", version, minimumVersion)
		}
		if got[i] > floor[i] {
			return nil
		}
	}
	return nil
}

type capabilityDocument struct {
	Protocol      int                    `json:"protocol"`
	SchemaVersion int                    `json:"schema_version"`
	Schemas       map[string]*schemaNode `json:"schemas"`
}

type schemaNode struct {
	Ref        string                 `json:"$ref"`
	Const      json.RawMessage        `json:"const"`
	OneOf      []*schemaNode          `json:"oneOf"`
	AnyOf      []*schemaNode          `json:"anyOf"`
	Properties map[string]*schemaNode `json:"properties"`
	Required   []string               `json:"required"`
	Defs       map[string]*schemaNode `json:"$defs"`
}

type methodRequirement struct {
	name       string
	required   []string
	properties []string
}

var requiredMethodCapabilities = []methodRequirement{
	{name: "server.stop"},
	{name: "server.agent_manifests"},
	{name: "session.snapshot"},
	{name: "workspace.create", properties: []string{"cwd", "env", "focus", "label"}},
	{name: "workspace.focus", required: []string{"workspace_id"}},
	{name: "workspace.report_metadata", required: []string{"workspace_id", "source", "tokens"}, properties: []string{"seq", "ttl_ms"}},
	{name: "workspace.close", required: []string{"workspace_id"}},
	{name: "worktree.list", properties: []string{"cwd", "workspace_id"}},
	{name: "worktree.create", properties: []string{"base", "branch", "cwd", "focus", "label", "path", "workspace_id"}},
	{name: "worktree.open", properties: []string{"branch", "cwd", "focus", "label", "path", "workspace_id"}},
	{name: "worktree.remove", required: []string{"workspace_id"}, properties: []string{"force"}},
	{name: "agent.list"},
	{name: "agent.read", required: []string{"target", "source"}, properties: []string{"format", "lines", "strip_ansi"}},
	{name: "agent.rename", required: []string{"target"}, properties: []string{"name"}},
	{name: "agent.focus", required: []string{"target"}},
	{name: "agent.prompt", required: []string{"target", "text"}, properties: []string{"wait"}},
	{name: "agent.wait", required: []string{"target"}, properties: []string{"timeout_ms", "until"}},
	{name: "pane.get", required: []string{"pane_id"}},
	{name: "pane.process_info", properties: []string{"pane_id"}},
	{name: "pane.read", required: []string{"pane_id", "source"}, properties: []string{"lines", "format", "strip_ansi"}},
	{name: "pane.send_input", required: []string{"pane_id"}, properties: []string{"keys", "text"}},
	{name: "pane.report_metadata", required: []string{"pane_id", "source"}, properties: []string{"tokens", "seq", "ttl_ms"}},
	{name: "pane.close", required: []string{"pane_id"}},
	{name: "pane.wait_for_output", required: []string{"pane_id", "source", "match"}, properties: []string{"lines", "strip_ansi", "timeout_ms"}},
	{name: "plugin.list", properties: []string{"plugin_id"}},
}

func validateCapabilitySchema(data []byte) error {
	var document capabilityDocument
	if err := decodeOne(data, &document); err != nil {
		return fmt.Errorf("parse herdr api schema --json: %w", err)
	}
	if document.Protocol != supportedProtocol || document.SchemaVersion != supportedSchema {
		return fmt.Errorf(
			"unsupported herdr API tuple protocol=%d schema_version=%d (required: protocol=%d schema_version=%d)",
			document.Protocol,
			document.SchemaVersion,
			supportedProtocol,
			supportedSchema,
		)
	}
	request := document.Schemas["request"]
	if request == nil {
		return fmt.Errorf("unsupported herdr request schema: missing schemas.request")
	}
	for _, requirement := range requiredMethodCapabilities {
		variant, err := findVariant(&document, request, "method", requirement.name)
		if err != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s: %w", requirement.name, err)
		}
		params, err := resolveSchema(&document, variant.Properties["params"])
		if err != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s params: %w", requirement.name, err)
		}
		if err := requireFields(params, requirement.required, requirement.properties); err != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s params: %w", requirement.name, err)
		}
	}

	success := document.Schemas["success_response"]
	if success == nil {
		return fmt.Errorf("unsupported herdr response schema: missing schemas.success_response")
	}
	result, err := resolveSchema(&document, success.Properties["result"])
	if err != nil {
		return fmt.Errorf("unsupported herdr response schema: result: %w", err)
	}
	for name, required := range map[string][]string{
		"session_snapshot":      {"snapshot"},
		"workspace_info":        {"workspace"},
		"workspace_created":     {"workspace", "tab", "root_pane"},
		"worktree_list":         {"source", "worktrees"},
		"worktree_created":      {"workspace", "tab", "root_pane", "worktree"},
		"worktree_opened":       {"workspace", "tab", "root_pane", "worktree", "already_open"},
		"worktree_removed":      {"workspace_id", "path", "forced"},
		"agent_list":            {"agents"},
		"agent_info":            {"agent"},
		"agent_manifest_status": {"manifests"},
		"agent_prompted":        {"agent"},
		"wait_matched":          {"event"},
		"pane_info":             {"pane"},
		"pane_process_info":     {"process_info"},
		"pane_read":             {"read"},
		"output_matched":        {"pane_id", "revision", "read"},
		"plugin_list":           {"plugins"},
		"ok":                    nil,
	} {
		variant, findErr := findVariant(&document, result, "type", name)
		if findErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, findErr)
		}
		if fieldErr := requireFields(variant, append([]string{"type"}, required...), nil); fieldErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, fieldErr)
		}
	}
	manifestVariant, _ := findVariant(&document, result, "type", "agent_manifest_status")
	err = requireFields(manifestVariant, []string{"type", "manifests"}, []string{"last_check_unix", "last_result"})
	if err != nil {
		return fmt.Errorf("unsupported herdr response schema: result agent_manifest_status: %w", err)
	}

	snapshotVariant, _ := findVariant(&document, result, "type", "session_snapshot")
	snapshot, err := resolveSchema(&document, snapshotVariant.Properties["snapshot"])
	if err != nil {
		return fmt.Errorf("unsupported herdr response schema: session snapshot: %w", err)
	}
	if err := requireFields(snapshot, []string{"version", "protocol", "workspaces", "tabs", "panes", "layouts", "agents"}, nil); err != nil {
		return fmt.Errorf("unsupported herdr response schema: session snapshot: %w", err)
	}
	for name, required := range map[string][]string{
		"WorkspaceInfo":          {"workspace_id", "number", "label", "focused", "pane_count", "tab_count", "active_tab_id", "agent_status"},
		"TabInfo":                {"tab_id", "workspace_id", "number", "label", "focused", "pane_count", "agent_status"},
		"WorkspaceWorktreeInfo":  {"repo_key", "repo_name", "repo_root", "checkout_path", "is_linked_worktree"},
		"WorktreeInfo":           {"path", "is_bare", "is_detached", "is_prunable", "is_linked_worktree", "label"},
		"PaneInfo":               {"pane_id", "terminal_id", "workspace_id", "tab_id", "focused", "agent_status", "revision"},
		"AgentInfo":              {"terminal_id", "workspace_id", "tab_id", "pane_id", "focused", "agent_status", "revision"},
		"PaneProcessInfo":        {"pane_id"},
		"PaneProcessInfoProcess": {"pid", "name"},
		"AgentSessionInfo":       {"source", "agent", "kind", "value"},
		"PaneReadResult":         {"pane_id", "workspace_id", "tab_id", "source", "format", "text", "revision", "truncated"},
		"AgentManifestInfo":      {"agent", "source", "source_kind", "local_override_shadowing_remote"},
	} {
		node := success.Defs[name]
		if node == nil {
			return fmt.Errorf("unsupported herdr response schema: missing %s", name)
		}
		if err := requireFields(node, required, nil); err != nil {
			return fmt.Errorf("unsupported herdr response schema: %s: %w", name, err)
		}
	}
	for name, properties := range map[string][]string{
		"WorktreeInfo":           {"branch", "open_workspace_id"},
		"PaneInfo":               {"agent", "cwd", "foreground_cwd", "agent_session"},
		"AgentInfo":              {"agent", "name", "cwd", "foreground_cwd", "agent_session", "interactive_ready", "launch_pending", "state_change_seq"},
		"PaneProcessInfo":        {"shell_pid", "foreground_process_group_id", "foreground_processes"},
		"PaneProcessInfoProcess": {"argv", "argv0", "cwd"},
	} {
		if err := requireFields(success.Defs[name], nil, properties); err != nil {
			return fmt.Errorf("unsupported herdr response schema: %s: %w", name, err)
		}
	}
	manifestInfo := success.Defs["AgentManifestInfo"]
	if err := requireFields(manifestInfo, nil, []string{
		"active_version", "cached_remote_version", "remote_last_checked_unix", "remote_update_error", "remote_update_result", "warning",
	}); err != nil {
		return fmt.Errorf("unsupported herdr response schema: AgentManifestInfo: %w", err)
	}
	return nil
}

func findVariant(document *capabilityDocument, node *schemaNode, property, value string) (*schemaNode, error) {
	resolved, err := resolveSchema(document, node)
	if err != nil {
		return nil, err
	}
	for _, variant := range resolved.OneOf {
		variant, err = resolveSchema(document, variant)
		if err != nil {
			return nil, err
		}
		candidate := variant.Properties[property]
		if candidate == nil {
			continue
		}
		var constant string
		if json.Unmarshal(candidate.Const, &constant) == nil && constant == value {
			return variant, nil
		}
	}
	return nil, fmt.Errorf("missing %s %q", property, value)
}

func resolveSchema(document *capabilityDocument, node *schemaNode) (*schemaNode, error) {
	if node == nil {
		return nil, fmt.Errorf("missing schema node")
	}
	seen := map[string]bool{}
	for node.Ref != "" {
		if seen[node.Ref] {
			return nil, fmt.Errorf("cyclic schema reference %q", node.Ref)
		}
		seen[node.Ref] = true
		parts := strings.Split(strings.TrimPrefix(node.Ref, "#/"), "/")
		if len(parts) != 4 || parts[0] != "schemas" || parts[2] != "$defs" {
			return nil, fmt.Errorf("unsupported schema reference %q", node.Ref)
		}
		root := document.Schemas[parts[1]]
		if root == nil || root.Defs[parts[3]] == nil {
			return nil, fmt.Errorf("unresolved schema reference %q", node.Ref)
		}
		node = root.Defs[parts[3]]
	}
	return node, nil
}

func requireFields(node *schemaNode, required, properties []string) error {
	if node == nil {
		return fmt.Errorf("missing object schema")
	}
	for _, field := range required {
		if !slices.Contains(node.Required, field) || node.Properties[field] == nil {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	for _, field := range properties {
		if node.Properties[field] == nil {
			return fmt.Errorf("missing field %q", field)
		}
	}
	return nil
}
