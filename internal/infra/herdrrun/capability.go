package herdrrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const minimumVersion = "0.7.4"

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// parseAdmittedVersion validates the public herdr --version output and returns
// the stable version used to bind later status and response checks.
func parseAdmittedVersion(output []byte) (string, error) {
	raw := strings.TrimSpace(string(output))
	version, ok := strings.CutPrefix(raw, "herdr ")
	if !ok || strings.TrimSpace(version) != version || strings.ContainsAny(version, " \t\r\n") {
		return "", fmt.Errorf(
			"unsupported herdr CLI version %q (required: herdr stable >=%s)",
			raw,
			minimumVersion,
		)
	}
	if err := validateAdmittedVersion(version); err != nil {
		return "", fmt.Errorf("unsupported herdr CLI version %q: %w", raw, err)
	}
	return version, nil
}

func validateAdmittedVersion(version string) error {
	got := stableVersionPattern.FindStringSubmatch(version)
	minimum := stableVersionPattern.FindStringSubmatch(minimumVersion)
	if got == nil || minimum == nil || compareVersionCore(got[1:4], minimum[1:4]) < 0 {
		return fmt.Errorf("required: stable >=%s", minimumVersion)
	}
	return nil
}

func compareVersionCore(left, right []string) int {
	for i := range left {
		if len(left[i]) != len(right[i]) {
			if len(left[i]) < len(right[i]) {
				return -1
			}
			return 1
		}
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

type capabilityDocument struct {
	Protocol      int                    `json:"protocol"`
	SchemaVersion int                    `json:"schema_version"`
	Schemas       map[string]*schemaNode `json:"schemas"`
}

type schemaTypes []string

func (types *schemaTypes) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*types = schemaTypes{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("schema type must be a string or string array: %w", err)
	}
	*types = many
	return nil
}

type schemaNode struct {
	Boolean              *bool                  `json:"-"`
	Ref                  string                 `json:"$ref,omitempty"`
	Types                schemaTypes            `json:"type,omitempty"`
	Const                *string                `json:"const,omitempty"`
	ConstIsString        bool                   `json:"-"`
	Enum                 []string               `json:"enum,omitempty"`
	EnumIsStrings        bool                   `json:"-"`
	Required             []string               `json:"required,omitempty"`
	Properties           map[string]*schemaNode `json:"properties,omitempty"`
	OneOf                []*schemaNode          `json:"oneOf,omitempty"`
	AnyOf                []*schemaNode          `json:"anyOf,omitempty"`
	Items                *schemaNode            `json:"items,omitempty"`
	AdditionalProperties *schemaNode            `json:"additionalProperties,omitempty"`
	Defs                 map[string]*schemaNode `json:"$defs,omitempty"`
}

func (node *schemaNode) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		node.Boolean = &value
		return nil
	}
	var decoded struct {
		Ref                  string                 `json:"$ref,omitempty"`
		Types                schemaTypes            `json:"type,omitempty"`
		Const                json.RawMessage        `json:"const,omitempty"`
		Enum                 []json.RawMessage      `json:"enum,omitempty"`
		Required             []string               `json:"required,omitempty"`
		Properties           map[string]*schemaNode `json:"properties,omitempty"`
		OneOf                []*schemaNode          `json:"oneOf,omitempty"`
		AnyOf                []*schemaNode          `json:"anyOf,omitempty"`
		Items                *schemaNode            `json:"items,omitempty"`
		AdditionalProperties *schemaNode            `json:"additionalProperties,omitempty"`
		Defs                 map[string]*schemaNode `json:"$defs,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*node = schemaNode{
		Ref:                  decoded.Ref,
		Types:                decoded.Types,
		Required:             decoded.Required,
		Properties:           decoded.Properties,
		OneOf:                decoded.OneOf,
		AnyOf:                decoded.AnyOf,
		Items:                decoded.Items,
		AdditionalProperties: decoded.AdditionalProperties,
		Defs:                 decoded.Defs,
	}
	if len(decoded.Const) > 0 {
		var value string
		if err := json.Unmarshal(decoded.Const, &value); err == nil {
			node.Const = &value
			node.ConstIsString = true
		}
	}
	if decoded.Enum != nil {
		node.EnumIsStrings = true
		for _, raw := range decoded.Enum {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				node.EnumIsStrings = false
				node.Enum = nil
				break
			}
			node.Enum = append(node.Enum, value)
		}
	}
	return nil
}

type schemaExpectation struct {
	Types                []string
	Const                string
	Enum                 []string
	Required             []string
	RejectExtraRequired  bool
	Properties           map[string]*schemaExpectation
	AnyOf                []*schemaExpectation
	Items                *schemaExpectation
	AdditionalProperties *schemaExpectation
}

func typed(types ...string) *schemaExpectation {
	return &schemaExpectation{Types: types}
}

func arrayOf(items *schemaExpectation) *schemaExpectation {
	return &schemaExpectation{Types: []string{"array"}, Items: items}
}

func objectShape(required []string, rejectExtra bool, properties map[string]*schemaExpectation) *schemaExpectation {
	return &schemaExpectation{
		Types:               []string{"object"},
		Required:            required,
		RejectExtraRequired: rejectExtra,
		Properties:          properties,
	}
}

func nullable(shape *schemaExpectation) *schemaExpectation {
	return &schemaExpectation{AnyOf: []*schemaExpectation{shape, typed("null")}}
}

func enumShape(values ...string) *schemaExpectation {
	return &schemaExpectation{Types: []string{"string"}, Enum: values}
}

type methodCapability struct {
	name   string
	params *schemaExpectation
}

// validateCapabilitySchema verifies the stable API tuple and the structural
// request/response contract used by fanout. Additive unrelated methods and
// optional fields remain forward compatible.
func validateCapabilitySchema(data []byte) error {
	var document capabilityDocument
	if err := decodeOne(data, &document); err != nil {
		return fmt.Errorf("decode capability schema: %w", err)
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
	if err := validateRequestCapabilities(&document); err != nil {
		return fmt.Errorf("unsupported herdr request schema: %w", err)
	}
	if err := validateResponseCapabilities(&document); err != nil {
		return fmt.Errorf("unsupported herdr response schema: %w", err)
	}
	return nil
}

func validateRequestCapabilities(document *capabilityDocument) error {
	request, err := namedSchema(document, "request")
	if err != nil {
		return err
	}
	if err := matchSchema(document, request, objectShape(
		[]string{"id"},
		true,
		map[string]*schemaExpectation{"id": typed("string")},
	), "request"); err != nil {
		return err
	}
	for _, method := range requiredMethods() {
		variant, err := findConstVariant(request.OneOf, "method", method.name)
		if err != nil {
			return fmt.Errorf("method %s: %w", method.name, err)
		}
		expected := objectShape(
			[]string{"method", "params"},
			true,
			map[string]*schemaExpectation{
				"method": {Types: []string{"string"}, Const: method.name},
				"params": method.params,
			},
		)
		if err := matchSchema(document, variant, expected, "request."+method.name); err != nil {
			return err
		}
	}
	return nil
}

func requiredMethods() []methodCapability {
	stringType := typed("string")
	nullableString := typed("string", "null")
	booleanType := typed("boolean")
	nullableInteger := typed("integer", "null")
	stringMap := &schemaExpectation{Types: []string{"object"}, AdditionalProperties: typed("string")}
	nullableStringMap := &schemaExpectation{Types: []string{"object"}, AdditionalProperties: typed("string", "null")}
	stringArray := arrayOf(typed("string"))

	empty := objectShape(nil, true, nil)
	workspaceTarget := objectShape([]string{"workspace_id"}, true, map[string]*schemaExpectation{"workspace_id": stringType})

	return []methodCapability{
		{name: "session.snapshot", params: empty},
		{name: "workspace.create", params: objectShape(nil, true, map[string]*schemaExpectation{
			"cwd": nullableString, "env": stringMap, "focus": booleanType, "label": nullableString,
		})},
		{name: "workspace.close", params: workspaceTarget},
		{name: "workspace.report_metadata", params: objectShape(
			[]string{"workspace_id", "source", "tokens"},
			true,
			map[string]*schemaExpectation{
				"workspace_id": stringType, "source": stringType, "tokens": nullableStringMap,
				"seq": nullableInteger, "ttl_ms": nullableInteger,
			},
		)},
		{name: "worktree.create", params: objectShape(nil, true, map[string]*schemaExpectation{
			"branch": nullableString, "base": nullableString, "path": nullableString,
			"workspace_id": nullableString, "cwd": nullableString, "label": nullableString, "focus": booleanType,
		})},
		{name: "worktree.open", params: objectShape(nil, true, map[string]*schemaExpectation{
			"branch": nullableString, "path": nullableString, "workspace_id": nullableString,
			"cwd": nullableString, "label": nullableString, "focus": booleanType,
		})},
		{name: "worktree.remove", params: objectShape(
			[]string{"workspace_id"},
			true,
			map[string]*schemaExpectation{"workspace_id": stringType, "force": booleanType},
		)},
		{name: "agent.start", params: objectShape(
			[]string{"name", "argv"},
			true,
			map[string]*schemaExpectation{
				"name": stringType, "argv": stringArray, "cwd": nullableString, "env": stringMap,
				"focus":  booleanType,
				"split":  nullable(enumShape("right", "down")),
				"tab_id": nullableString, "workspace_id": nullableString,
			},
		)},
		{name: "pane.process_info", params: objectShape(nil, true, map[string]*schemaExpectation{"pane_id": nullableString})},
		{name: "pane.report_metadata", params: objectShape(
			[]string{"pane_id", "source"},
			true,
			map[string]*schemaExpectation{
				"pane_id": stringType, "source": stringType, "agent": nullableString,
				"applies_to_source": nullableString, "clear_display_agent": booleanType,
				"clear_state_labels": booleanType, "clear_title": booleanType,
				"display_agent": nullableString, "seq": nullableInteger, "state_labels": stringMap,
				"title": nullableString, "tokens": nullableStringMap, "ttl_ms": nullableInteger,
			},
		)},
		{name: "pane.send_text", params: objectShape(
			[]string{"pane_id", "text"},
			true,
			map[string]*schemaExpectation{"pane_id": stringType, "text": stringType},
		)},
		{name: "pane.send_keys", params: objectShape(
			[]string{"pane_id", "keys"},
			true,
			map[string]*schemaExpectation{"pane_id": stringType, "keys": stringArray},
		)},
	}
}

func validateResponseCapabilities(document *capabilityDocument) error {
	success, schemaErr := namedSchema(document, "success_response")
	if schemaErr != nil {
		return schemaErr
	}
	if matchErr := matchSchema(document, success, objectShape(
		[]string{"id", "result"},
		false,
		map[string]*schemaExpectation{"id": typed("string"), "result": {}},
	), "success_response"); matchErr != nil {
		return matchErr
	}
	result, resolveErr := dereference(document, success.Properties["result"])
	if resolveErr != nil {
		return resolveErr
	}
	for name, expected := range requiredResultVariants() {
		variant, findErr := findConstVariant(result.OneOf, "type", name)
		if findErr != nil {
			return fmt.Errorf("result %s: %w", name, findErr)
		}
		if matchErr := matchSchema(document, variant, expected, "result."+name); matchErr != nil {
			return matchErr
		}
	}

	errorResponse, schemaErr := namedSchema(document, "error_response")
	if schemaErr != nil {
		return schemaErr
	}
	return matchSchema(document, errorResponse, objectShape(
		[]string{"id", "error"},
		false,
		map[string]*schemaExpectation{
			"id": typed("string"),
			"error": objectShape(
				[]string{"code", "message"},
				false,
				map[string]*schemaExpectation{"code": typed("string"), "message": typed("string")},
			),
		},
	), "error_response")
}

func requiredResultVariants() map[string]*schemaExpectation {
	stringType := typed("string")
	integerType := typed("integer")
	booleanType := typed("boolean")
	nullableString := typed("string", "null")
	stringArray := arrayOf(stringType)
	agentStatus := enumShape("idle", "working", "blocked", "done", "unknown")
	agentSession := objectShape(
		[]string{"source", "agent", "kind", "value"},
		false,
		map[string]*schemaExpectation{
			"source": stringType, "agent": stringType, "kind": enumShape("id", "path"), "value": stringType,
		},
	)
	workspaceWorktree := objectShape(
		[]string{"repo_key", "repo_name", "repo_root", "checkout_path", "is_linked_worktree"},
		false,
		map[string]*schemaExpectation{
			"repo_key": stringType, "repo_name": stringType, "repo_root": stringType,
			"checkout_path": stringType, "is_linked_worktree": booleanType,
		},
	)
	workspace := objectShape(
		[]string{"workspace_id", "number", "label", "focused", "pane_count", "tab_count", "active_tab_id", "agent_status"},
		false,
		map[string]*schemaExpectation{
			"workspace_id": stringType, "number": integerType, "label": stringType, "focused": booleanType,
			"pane_count": integerType, "tab_count": integerType, "active_tab_id": stringType,
			"agent_status": agentStatus, "worktree": nullable(workspaceWorktree),
		},
	)
	tab := objectShape(
		[]string{"tab_id", "workspace_id", "number", "label", "focused", "pane_count", "agent_status"},
		false,
		map[string]*schemaExpectation{
			"tab_id": stringType, "workspace_id": stringType, "number": integerType, "label": stringType,
			"focused": booleanType, "pane_count": integerType, "agent_status": agentStatus,
		},
	)
	pane := objectShape(
		[]string{"pane_id", "terminal_id", "workspace_id", "tab_id", "focused", "agent_status", "revision"},
		false,
		map[string]*schemaExpectation{
			"pane_id": stringType, "terminal_id": stringType, "workspace_id": stringType,
			"tab_id": stringType, "focused": booleanType, "agent_status": agentStatus,
			"revision": integerType, "cwd": nullableString, "agent_session": nullable(agentSession),
		},
	)
	agent := objectShape(
		[]string{"terminal_id", "agent_status", "workspace_id", "tab_id", "pane_id", "focused", "revision"},
		false,
		map[string]*schemaExpectation{
			"terminal_id": stringType, "agent_status": agentStatus, "workspace_id": stringType,
			"tab_id": stringType, "pane_id": stringType, "focused": booleanType, "revision": integerType,
			"name": nullableString, "agent": nullableString, "agent_session": nullable(agentSession),
		},
	)
	worktree := objectShape(
		[]string{"path", "is_bare", "is_detached", "is_prunable", "is_linked_worktree", "label"},
		false,
		map[string]*schemaExpectation{
			"path": stringType, "branch": nullableString, "label": stringType,
			"is_bare": booleanType, "is_detached": booleanType, "is_prunable": booleanType,
			"is_linked_worktree": booleanType, "open_workspace_id": nullableString,
		},
	)
	process := objectShape(
		[]string{"pid", "name"},
		false,
		map[string]*schemaExpectation{
			"pid": integerType, "name": stringType,
			"argv": {Types: []string{"array", "null"}, Items: stringType},
			"cwd":  nullableString,
		},
	)
	processInfo := objectShape(
		[]string{"pane_id"},
		false,
		map[string]*schemaExpectation{
			"pane_id": stringType, "shell_pid": typed("integer", "null"),
			"foreground_process_group_id": typed("integer", "null"),
			"foreground_processes":        arrayOf(process),
		},
	)
	layoutRect := objectShape(
		[]string{"x", "y", "width", "height"},
		false,
		map[string]*schemaExpectation{
			"x": integerType, "y": integerType, "width": integerType, "height": integerType,
		},
	)
	layoutPane := objectShape(
		[]string{"pane_id", "focused", "rect"},
		false,
		map[string]*schemaExpectation{"pane_id": stringType, "focused": booleanType, "rect": layoutRect},
	)
	layoutSplit := objectShape(
		[]string{"id", "direction", "ratio", "rect"},
		false,
		map[string]*schemaExpectation{
			"id": stringType, "direction": enumShape("right", "down"), "ratio": typed("number"), "rect": layoutRect,
		},
	)
	layout := objectShape(
		[]string{"workspace_id", "tab_id", "zoomed", "area", "focused_pane_id", "panes", "splits"},
		false,
		map[string]*schemaExpectation{
			"workspace_id": stringType, "tab_id": stringType, "zoomed": booleanType,
			"area": layoutRect, "focused_pane_id": stringType,
			"panes": arrayOf(layoutPane), "splits": arrayOf(layoutSplit),
		},
	)
	snapshot := objectShape(
		[]string{"version", "protocol", "workspaces", "tabs", "panes", "layouts", "agents"},
		false,
		map[string]*schemaExpectation{
			"version": stringType, "protocol": integerType,
			"workspaces": arrayOf(workspace), "tabs": arrayOf(tab), "panes": arrayOf(pane),
			"layouts": arrayOf(layout), "agents": arrayOf(agent),
			"focused_workspace_id": nullableString, "focused_tab_id": nullableString, "focused_pane_id": nullableString,
		},
	)

	variant := func(name string, required []string, properties map[string]*schemaExpectation) *schemaExpectation {
		properties["type"] = &schemaExpectation{Types: []string{"string"}, Const: name}
		return objectShape(append([]string{"type"}, required...), false, properties)
	}
	return map[string]*schemaExpectation{
		"session_snapshot": variant("session_snapshot", []string{"snapshot"}, map[string]*schemaExpectation{"snapshot": snapshot}),
		"workspace_created": variant("workspace_created", []string{"workspace", "tab", "root_pane"}, map[string]*schemaExpectation{
			"workspace": workspace, "tab": tab, "root_pane": pane,
		}),
		"worktree_created": variant("worktree_created", []string{"workspace", "tab", "root_pane", "worktree"}, map[string]*schemaExpectation{
			"workspace": workspace, "tab": tab, "root_pane": pane, "worktree": worktree,
		}),
		"worktree_opened": variant("worktree_opened", []string{"workspace", "tab", "root_pane", "worktree", "already_open"}, map[string]*schemaExpectation{
			"workspace": workspace, "tab": tab, "root_pane": pane, "worktree": worktree, "already_open": booleanType,
		}),
		"worktree_removed": variant("worktree_removed", []string{"workspace_id", "path", "forced"}, map[string]*schemaExpectation{
			"workspace_id": stringType, "path": stringType, "forced": booleanType,
		}),
		"agent_started": variant("agent_started", []string{"agent", "argv"}, map[string]*schemaExpectation{
			"agent": agent, "argv": stringArray,
		}),
		"pane_process_info": variant("pane_process_info", []string{"process_info"}, map[string]*schemaExpectation{"process_info": processInfo}),
		"ok":                variant("ok", nil, map[string]*schemaExpectation{}),
	}
}

func namedSchema(document *capabilityDocument, name string) (*schemaNode, error) {
	node := document.Schemas[name]
	if node == nil {
		return nil, fmt.Errorf("missing schemas.%s", name)
	}
	return node, nil
}

func findConstVariant(variants []*schemaNode, property, value string) (*schemaNode, error) {
	var found *schemaNode
	for _, variant := range variants {
		candidate := variant.Properties[property]
		if candidate == nil || candidate.Const == nil || *candidate.Const != value {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate %s=%q variant", property, value)
		}
		found = variant
	}
	if found == nil {
		return nil, fmt.Errorf("missing %s=%q variant", property, value)
	}
	return found, nil
}

func dereference(document *capabilityDocument, node *schemaNode) (*schemaNode, error) {
	seen := make(map[string]bool)
	for node != nil && node.Ref != "" {
		if seen[node.Ref] {
			return nil, fmt.Errorf("cyclic schema reference %q", node.Ref)
		}
		seen[node.Ref] = true
		parts := strings.Split(strings.TrimPrefix(node.Ref, "#/schemas/"), "/")
		if !strings.HasPrefix(node.Ref, "#/schemas/") || len(parts) != 3 || parts[1] != "$defs" {
			return nil, fmt.Errorf("unsupported schema reference %q", node.Ref)
		}
		root := document.Schemas[parts[0]]
		if root == nil || root.Defs[parts[2]] == nil {
			return nil, fmt.Errorf("unresolvable schema reference %q", node.Ref)
		}
		node = root.Defs[parts[2]]
	}
	if node == nil {
		return nil, fmt.Errorf("missing schema node")
	}
	return node, nil
}

func matchSchema(document *capabilityDocument, node *schemaNode, expected *schemaExpectation, path string) error {
	resolved, err := dereference(document, node)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if resolved.Boolean != nil {
		return fmt.Errorf("%s is a boolean schema", path)
	}
	if len(expected.Types) > 0 && !sameStrings(resolved.Types, expected.Types) {
		return fmt.Errorf("%s type=%v, want %v", path, resolved.Types, expected.Types)
	}
	if expected.Const != "" && (!resolved.ConstIsString || resolved.Const == nil || *resolved.Const != expected.Const) {
		return fmt.Errorf("%s const=%v, want %q", path, resolved.Const, expected.Const)
	}
	if expected.Enum != nil && (!resolved.EnumIsStrings || !sameStrings(resolved.Enum, expected.Enum)) {
		return fmt.Errorf("%s enum=%v, want %v", path, resolved.Enum, expected.Enum)
	}
	if err := requireFields(resolved.Required, expected.Required, expected.RejectExtraRequired, path); err != nil {
		return err
	}
	for name, propertyExpectation := range expected.Properties {
		property := resolved.Properties[name]
		if property == nil {
			return fmt.Errorf("%s is missing property %q", path, name)
		}
		if err := matchSchema(document, property, propertyExpectation, path+"."+name); err != nil {
			return err
		}
	}
	if expected.Items != nil {
		if resolved.Items == nil {
			return fmt.Errorf("%s is missing items", path)
		}
		if err := matchSchema(document, resolved.Items, expected.Items, path+"[]"); err != nil {
			return err
		}
	}
	if expected.AdditionalProperties != nil {
		if resolved.AdditionalProperties == nil {
			return fmt.Errorf("%s is missing additionalProperties", path)
		}
		if err := matchSchema(document, resolved.AdditionalProperties, expected.AdditionalProperties, path+".*"); err != nil {
			return err
		}
	}
	if len(expected.AnyOf) > 0 {
		if len(resolved.AnyOf) != len(expected.AnyOf) {
			return fmt.Errorf("%s anyOf has %d branches, want %d", path, len(resolved.AnyOf), len(expected.AnyOf))
		}
		used := make([]bool, len(resolved.AnyOf))
		for _, wanted := range expected.AnyOf {
			matched := false
			for i, candidate := range resolved.AnyOf {
				if used[i] || matchSchema(document, candidate, wanted, path+".anyOf") != nil {
					continue
				}
				used[i] = true
				matched = true
				break
			}
			if !matched {
				return fmt.Errorf("%s has no compatible anyOf branch", path)
			}
		}
	}
	return nil
}

func requireFields(actual, expected []string, rejectExtra bool, path string) error {
	seen := make(map[string]bool, len(actual))
	for _, name := range actual {
		if seen[name] {
			return fmt.Errorf("%s required contains duplicate %q", path, name)
		}
		seen[name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			return fmt.Errorf("%s is missing required field %q", path, name)
		}
	}
	if rejectExtra && len(actual) != len(expected) {
		return fmt.Errorf("%s required=%v, want %v", path, actual, expected)
	}
	return nil
}

func sameStrings(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
