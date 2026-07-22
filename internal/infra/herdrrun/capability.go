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
	{name: "session.snapshot"},
	{name: "workspace.focus", required: []string{"workspace_id"}},
	{name: "workspace.close", required: []string{"workspace_id"}},
	{name: "worktree.remove", required: []string{"workspace_id"}, properties: []string{"force"}},
	{name: "agent.prompt", required: []string{"target", "text"}},
	{name: "pane.read", required: []string{"pane_id", "source"}, properties: []string{"lines", "format"}},
	{name: "pane.close", required: []string{"pane_id"}},
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
		"session_snapshot": {"snapshot"},
		"worktree_removed": {"workspace_id", "path", "forced"},
		"agent_prompted":   {"agent"},
		"pane_read":        {"read"},
		"ok":               nil,
	} {
		variant, findErr := findVariant(&document, result, "type", name)
		if findErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, findErr)
		}
		if fieldErr := requireFields(variant, append([]string{"type"}, required...), nil); fieldErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, fieldErr)
		}
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
		"WorkspaceInfo":         {"workspace_id", "label", "focused", "active_tab_id", "agent_status"},
		"WorkspaceWorktreeInfo": {"repo_key", "repo_root", "checkout_path", "is_linked_worktree"},
		"PaneInfo":              {"pane_id", "terminal_id", "workspace_id", "tab_id", "focused", "agent_status", "revision"},
		"AgentInfo":             {"terminal_id", "workspace_id", "tab_id", "pane_id", "focused", "agent_status", "revision"},
		"AgentSessionInfo":      {"source", "agent", "kind", "value"},
	} {
		node := success.Defs[name]
		if node == nil {
			return fmt.Errorf("unsupported herdr response schema: missing %s", name)
		}
		if err := requireFields(node, required, nil); err != nil {
			return fmt.Errorf("unsupported herdr response schema: %s: %w", name, err)
		}
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
