package herdrrun

import (
	"encoding/json"
	"fmt"
	"slices"
)

type schemaShape struct {
	ref                  string
	types                []string
	enum                 []string
	constant             string
	hasConstant          bool
	required             []string
	exactRequired        bool
	properties           map[string]schemaShape
	items                *schemaShape
	additionalProperties *schemaShape
	oneOf                []schemaShape
	anyOf                []schemaShape
}

func typedShape(types ...string) schemaShape {
	return schemaShape{types: types}
}

func refShape(ref string) schemaShape {
	return schemaShape{ref: ref}
}

func enumShape(values ...string) schemaShape {
	return schemaShape{types: []string{"string"}, enum: values}
}

func constStringShape(value string) schemaShape {
	return schemaShape{types: []string{"string"}, constant: value, hasConstant: true}
}

func arrayShape(item schemaShape) schemaShape {
	return schemaShape{types: []string{"array"}, items: &item}
}

func mapShape(value schemaShape) schemaShape {
	return schemaShape{types: []string{"object"}, additionalProperties: &value}
}

func anyOfShape(shapes ...schemaShape) schemaShape {
	return schemaShape{anyOf: shapes}
}

func oneOfShape(shapes ...schemaShape) schemaShape {
	return schemaShape{oneOf: shapes}
}

func objectShape(required []string, exact bool, properties map[string]schemaShape) schemaShape {
	return schemaShape{types: []string{"object"}, required: required, exactRequired: exact, properties: properties}
}

var requestMethodDefinitions = map[string]string{
	"server.stop": "EmptyParams", "server.agent_manifests": "EmptyParams", "session.snapshot": "EmptyParams",
	"workspace.create": "WorkspaceCreateParams", "workspace.focus": "WorkspaceTarget",
	"workspace.report_metadata": "WorkspaceReportMetadataParams", "workspace.close": "WorkspaceTarget",
	"worktree.list": "WorktreeListParams", "worktree.create": "WorktreeCreateParams",
	"worktree.open": "WorktreeOpenParams", "worktree.remove": "WorktreeRemoveParams",
	"agent.list": "EmptyParams", "agent.read": "AgentReadParams", "agent.rename": "AgentRenameParams",
	"agent.focus": "AgentTarget", "agent.prompt": "AgentPromptParams", "agent.wait": "AgentWaitParams",
	"pane.get": "PaneTarget", "pane.process_info": "PaneProcessInfoParams", "pane.read": "PaneReadParams",
	"pane.send_input": "PaneSendInputParams", "pane.report_metadata": "PaneReportMetadataParams",
	"pane.close": "PaneTarget", "pane.wait_for_output": "PaneWaitForOutputParams", "plugin.list": "PluginListParams",
}

var requestDefinitionShapes = map[string]schemaShape{
	"EmptyParams": objectShape(nil, true, nil),
	"WorkspaceCreateParams": objectShape(nil, true, map[string]schemaShape{
		"cwd": typedShape("string", "null"), "env": mapShape(typedShape("string")),
		"focus": typedShape("boolean"), "label": typedShape("string", "null"),
	}),
	"WorkspaceTarget": objectShape([]string{"workspace_id"}, true, map[string]schemaShape{"workspace_id": typedShape("string")}),
	"WorkspaceReportMetadataParams": objectShape([]string{"workspace_id", "source", "tokens"}, true, map[string]schemaShape{
		"workspace_id": typedShape("string"), "source": typedShape("string"), "tokens": mapShape(typedShape("string", "null")),
		"seq": typedShape("integer", "null"), "ttl_ms": typedShape("integer", "null"),
	}),
	"WorktreeListParams": objectShape(nil, true, map[string]schemaShape{
		"cwd": typedShape("string", "null"), "workspace_id": typedShape("string", "null"),
	}),
	"WorktreeCreateParams": objectShape(nil, true, map[string]schemaShape{
		"base": typedShape("string", "null"), "branch": typedShape("string", "null"), "cwd": typedShape("string", "null"),
		"focus": typedShape("boolean"), "label": typedShape("string", "null"), "path": typedShape("string", "null"),
		"workspace_id": typedShape("string", "null"),
	}),
	"WorktreeOpenParams": objectShape(nil, true, map[string]schemaShape{
		"branch": typedShape("string", "null"), "cwd": typedShape("string", "null"), "focus": typedShape("boolean"),
		"label": typedShape("string", "null"), "path": typedShape("string", "null"), "workspace_id": typedShape("string", "null"),
	}),
	"WorktreeRemoveParams": objectShape([]string{"workspace_id"}, true, map[string]schemaShape{
		"workspace_id": typedShape("string"), "force": typedShape("boolean"),
	}),
	"AgentReadParams": objectShape([]string{"target", "source"}, true, map[string]schemaShape{
		"target": typedShape("string"), "source": refShape("#/schemas/request/$defs/ReadSource"),
		"format": refShape("#/schemas/request/$defs/ReadFormat"), "lines": typedShape("integer", "null"),
		"strip_ansi": typedShape("boolean"),
	}),
	"AgentRenameParams": objectShape([]string{"target"}, true, map[string]schemaShape{
		"target": typedShape("string"), "name": typedShape("string", "null"),
	}),
	"AgentTarget": objectShape([]string{"target"}, true, map[string]schemaShape{"target": typedShape("string")}),
	"AgentPromptParams": objectShape([]string{"target", "text"}, true, map[string]schemaShape{
		"target": typedShape("string"), "text": typedShape("string"),
		"wait": anyOfShape(refShape("#/schemas/request/$defs/AgentPromptWaitOptions"), typedShape("null")),
	}),
	"AgentWaitParams": objectShape([]string{"target"}, true, map[string]schemaShape{
		"target": typedShape("string"), "timeout_ms": typedShape("integer", "null"),
		"until": arrayShape(refShape("#/schemas/request/$defs/AgentStatus")),
	}),
	"PaneTarget":            objectShape([]string{"pane_id"}, true, map[string]schemaShape{"pane_id": typedShape("string")}),
	"PaneProcessInfoParams": objectShape(nil, true, map[string]schemaShape{"pane_id": typedShape("string", "null")}),
	"PaneReadParams": objectShape([]string{"pane_id", "source"}, true, map[string]schemaShape{
		"pane_id": typedShape("string"), "source": refShape("#/schemas/request/$defs/ReadSource"),
		"lines": typedShape("integer", "null"), "format": refShape("#/schemas/request/$defs/ReadFormat"),
		"strip_ansi": typedShape("boolean"),
	}),
	"PaneSendInputParams": objectShape([]string{"pane_id"}, true, map[string]schemaShape{
		"pane_id": typedShape("string"), "keys": arrayShape(typedShape("string")), "text": typedShape("string"),
	}),
	"PaneReportMetadataParams": objectShape([]string{"pane_id", "source"}, true, map[string]schemaShape{
		"pane_id": typedShape("string"), "source": typedShape("string"), "tokens": mapShape(typedShape("string", "null")),
		"seq": typedShape("integer", "null"), "ttl_ms": typedShape("integer", "null"),
	}),
	"PaneWaitForOutputParams": objectShape([]string{"pane_id", "source", "match"}, true, map[string]schemaShape{
		"pane_id": typedShape("string"), "source": refShape("#/schemas/request/$defs/ReadSource"),
		"match": refShape("#/schemas/request/$defs/OutputMatch"), "lines": typedShape("integer", "null"),
		"strip_ansi": typedShape("boolean"), "timeout_ms": typedShape("integer", "null"),
	}),
	"PluginListParams": objectShape(nil, true, map[string]schemaShape{"plugin_id": typedShape("string", "null")}),
	"AgentPromptWaitOptions": objectShape(nil, true, map[string]schemaShape{
		"timeout_ms": typedShape("integer", "null"), "until": arrayShape(refShape("#/schemas/request/$defs/AgentStatus")),
	}),
	"AgentStatus": enumShape("idle", "working", "blocked", "done", "unknown"),
	"ReadFormat":  enumShape("text", "ansi"),
	"ReadSource":  enumShape("visible", "recent", "recent_unwrapped", "detection"),
	"OutputMatch": oneOfShape(
		objectShape([]string{"type", "value"}, true, map[string]schemaShape{"type": constStringShape("substring"), "value": typedShape("string")}),
		objectShape([]string{"type", "value"}, true, map[string]schemaShape{"type": constStringShape("regex"), "value": typedShape("string")}),
	),
}

var successResultShapes = map[string]schemaShape{
	"session_snapshot": objectShape([]string{"type", "snapshot"}, false, map[string]schemaShape{
		"type": constStringShape("session_snapshot"), "snapshot": refShape("#/schemas/success_response/$defs/SessionSnapshot"),
	}),
	"workspace_info": objectShape([]string{"type", "workspace"}, false, map[string]schemaShape{
		"type": constStringShape("workspace_info"), "workspace": refShape("#/schemas/success_response/$defs/WorkspaceInfo"),
	}),
	"workspace_created": createdWorkspaceResultShape("workspace_created"),
	"worktree_created":  worktreeWorkspaceResultShape("worktree_created", false),
	"worktree_opened":   worktreeWorkspaceResultShape("worktree_opened", true),
	"worktree_list": objectShape([]string{"type", "source", "worktrees"}, false, map[string]schemaShape{
		"type": constStringShape("worktree_list"), "source": refShape("#/schemas/success_response/$defs/WorktreeSourceInfo"),
		"worktrees": arrayShape(refShape("#/schemas/success_response/$defs/WorktreeInfo")),
	}),
	"worktree_removed": objectShape([]string{"type", "workspace_id", "path", "forced"}, false, map[string]schemaShape{
		"type": constStringShape("worktree_removed"), "workspace_id": typedShape("string"),
		"path": typedShape("string"), "forced": typedShape("boolean"),
	}),
	"agent_list": objectShape([]string{"type", "agents"}, false, map[string]schemaShape{
		"type": constStringShape("agent_list"), "agents": arrayShape(refShape("#/schemas/success_response/$defs/AgentInfo")),
	}),
	"agent_info":     agentResultShape("agent_info"),
	"agent_prompted": agentResultShape("agent_prompted"),
	"wait_matched": objectShape([]string{"type", "event"}, false, map[string]schemaShape{
		"type": constStringShape("wait_matched"), "event": refShape("#/schemas/success_response/$defs/EventEnvelope"),
	}),
	"pane_info": objectShape([]string{"type", "pane"}, false, map[string]schemaShape{
		"type": constStringShape("pane_info"), "pane": refShape("#/schemas/success_response/$defs/PaneInfo"),
	}),
	"pane_process_info": objectShape([]string{"type", "process_info"}, false, map[string]schemaShape{
		"type": constStringShape("pane_process_info"), "process_info": refShape("#/schemas/success_response/$defs/PaneProcessInfo"),
	}),
	"pane_read": objectShape([]string{"type", "read"}, false, map[string]schemaShape{
		"type": constStringShape("pane_read"), "read": refShape("#/schemas/success_response/$defs/PaneReadResult"),
	}),
	"output_matched": objectShape([]string{"type", "pane_id", "revision", "read"}, false, map[string]schemaShape{
		"type": constStringShape("output_matched"), "pane_id": typedShape("string"), "revision": typedShape("integer"),
		"read": refShape("#/schemas/success_response/$defs/PaneReadResult"),
	}),
	"agent_manifest_status": objectShape([]string{"type", "manifests"}, false, map[string]schemaShape{
		"type":            constStringShape("agent_manifest_status"),
		"manifests":       arrayShape(refShape("#/schemas/success_response/$defs/AgentManifestInfo")),
		"last_check_unix": typedShape("integer", "null"), "last_result": typedShape("string", "null"),
	}),
	"plugin_list": objectShape([]string{"type", "plugins"}, false, map[string]schemaShape{
		"type":    constStringShape("plugin_list"),
		"plugins": arrayShape(refShape("#/schemas/success_response/$defs/InstalledPluginInfo")),
	}),
	"ok": objectShape([]string{"type"}, false, map[string]schemaShape{"type": constStringShape("ok")}),
}

func createdWorkspaceResultShape(resultType string) schemaShape {
	return objectShape([]string{"type", "workspace", "tab", "root_pane"}, false, map[string]schemaShape{
		"type": constStringShape(resultType), "workspace": refShape("#/schemas/success_response/$defs/WorkspaceInfo"),
		"tab": refShape("#/schemas/success_response/$defs/TabInfo"), "root_pane": refShape("#/schemas/success_response/$defs/PaneInfo"),
	})
}

func worktreeWorkspaceResultShape(resultType string, opened bool) schemaShape {
	shape := createdWorkspaceResultShape(resultType)
	shape.required = append(shape.required, "worktree")
	shape.properties["worktree"] = refShape("#/schemas/success_response/$defs/WorktreeInfo")
	if opened {
		shape.required = append(shape.required, "already_open")
		shape.properties["already_open"] = typedShape("boolean")
	}
	return shape
}

func agentResultShape(resultType string) schemaShape {
	return objectShape([]string{"type", "agent"}, false, map[string]schemaShape{
		"type": constStringShape(resultType), "agent": refShape("#/schemas/success_response/$defs/AgentInfo"),
	})
}

var successDefinitionShapes = map[string]schemaShape{
	"SessionSnapshot": objectShape([]string{"version", "protocol", "workspaces", "tabs", "panes", "layouts", "agents"}, false, map[string]schemaShape{
		"version": typedShape("string"), "protocol": typedShape("integer"),
		"workspaces": arrayShape(refShape("#/schemas/success_response/$defs/WorkspaceInfo")),
		"tabs":       arrayShape(refShape("#/schemas/success_response/$defs/TabInfo")),
		"panes":      arrayShape(refShape("#/schemas/success_response/$defs/PaneInfo")),
		"layouts":    typedShape("array"), "agents": arrayShape(refShape("#/schemas/success_response/$defs/AgentInfo")),
	}),
	"WorkspaceInfo": objectShape([]string{"workspace_id", "number", "label", "focused", "pane_count", "tab_count", "active_tab_id", "agent_status"}, false, map[string]schemaShape{
		"workspace_id": typedShape("string"), "number": typedShape("integer"), "label": typedShape("string"),
		"focused": typedShape("boolean"), "pane_count": typedShape("integer"), "tab_count": typedShape("integer"),
		"active_tab_id": typedShape("string"), "agent_status": refShape("#/schemas/success_response/$defs/AgentStatus"),
		"worktree": anyOfShape(refShape("#/schemas/success_response/$defs/WorkspaceWorktreeInfo"), typedShape("null")),
	}),
	"TabInfo": objectShape([]string{"tab_id", "workspace_id", "number", "label", "focused", "pane_count", "agent_status"}, false, map[string]schemaShape{
		"tab_id": typedShape("string"), "workspace_id": typedShape("string"), "number": typedShape("integer"),
		"label": typedShape("string"), "focused": typedShape("boolean"), "pane_count": typedShape("integer"),
		"agent_status": refShape("#/schemas/success_response/$defs/AgentStatus"),
	}),
	"WorkspaceWorktreeInfo": stringBooleanObjectShape(
		[]string{"repo_key", "repo_name", "repo_root", "checkout_path", "is_linked_worktree"},
		[]string{"repo_key", "repo_name", "repo_root", "checkout_path"}, []string{"is_linked_worktree"},
	),
	"WorktreeSourceInfo": objectShape([]string{"repo_key", "repo_name", "repo_root", "source_checkout_path"}, false, map[string]schemaShape{
		"repo_key": typedShape("string"), "repo_name": typedShape("string"), "repo_root": typedShape("string"),
		"source_checkout_path": typedShape("string"), "source_workspace_id": typedShape("string", "null"),
	}),
	"WorktreeInfo": objectShape([]string{"path", "is_bare", "is_detached", "is_prunable", "is_linked_worktree", "label"}, false, map[string]schemaShape{
		"path": typedShape("string"), "branch": typedShape("string", "null"), "is_bare": typedShape("boolean"),
		"is_detached": typedShape("boolean"), "is_prunable": typedShape("boolean"), "is_linked_worktree": typedShape("boolean"),
		"open_workspace_id": typedShape("string", "null"), "label": typedShape("string"),
	}),
	"PaneInfo":  paneInfoShape(),
	"AgentInfo": agentInfoShape(),
	"PaneProcessInfo": objectShape([]string{"pane_id"}, false, map[string]schemaShape{
		"pane_id": typedShape("string"), "shell_pid": typedShape("integer", "null"),
		"foreground_process_group_id": typedShape("integer", "null"),
		"foreground_processes":        arrayShape(refShape("#/schemas/success_response/$defs/PaneProcessInfoProcess")),
	}),
	"PaneProcessInfoProcess": objectShape([]string{"pid", "name"}, false, map[string]schemaShape{
		"pid": typedShape("integer"), "name": typedShape("string"), "argv": typedShape("array", "null"),
		"argv0": typedShape("string", "null"), "cwd": typedShape("string", "null"),
	}),
	"AgentSessionInfo": objectShape([]string{"source", "agent", "kind", "value"}, false, map[string]schemaShape{
		"source": typedShape("string"), "agent": typedShape("string"),
		"kind": refShape("#/schemas/success_response/$defs/AgentSessionRefKind"), "value": typedShape("string"),
	}),
	"PaneReadResult": objectShape([]string{"pane_id", "workspace_id", "tab_id", "source", "format", "text", "revision", "truncated"}, false, map[string]schemaShape{
		"pane_id": typedShape("string"), "workspace_id": typedShape("string"), "tab_id": typedShape("string"),
		"source": refShape("#/schemas/success_response/$defs/ReadSource"), "format": refShape("#/schemas/success_response/$defs/ReadFormat"),
		"text": typedShape("string"), "revision": typedShape("integer"), "truncated": typedShape("boolean"),
	}),
	"AgentManifestInfo": objectShape([]string{"agent", "source", "source_kind", "local_override_shadowing_remote"}, false, map[string]schemaShape{
		"agent": typedShape("string"), "source": typedShape("string"), "source_kind": typedShape("string"),
		"local_override_shadowing_remote": typedShape("boolean"), "active_version": typedShape("string", "null"),
		"cached_remote_version": typedShape("string", "null"), "remote_last_checked_unix": typedShape("integer", "null"),
		"remote_update_error": typedShape("string", "null"), "remote_update_result": typedShape("string", "null"),
		"warning": typedShape("string", "null"),
	}),
	"AgentStatus":         enumShape("idle", "working", "blocked", "done", "unknown"),
	"AgentSessionRefKind": enumShape("id", "path"),
	"ReadFormat":          enumShape("text", "ansi"),
	"ReadSource":          enumShape("visible", "recent", "recent_unwrapped", "detection"),
}

func stringBooleanObjectShape(required, stringsFields, booleanFields []string) schemaShape {
	properties := make(map[string]schemaShape, len(stringsFields)+len(booleanFields))
	for _, field := range stringsFields {
		properties[field] = typedShape("string")
	}
	for _, field := range booleanFields {
		properties[field] = typedShape("boolean")
	}
	return objectShape(required, false, properties)
}

func paneInfoShape() schemaShape {
	return objectShape([]string{"pane_id", "terminal_id", "workspace_id", "tab_id", "focused", "agent_status", "revision"}, false, map[string]schemaShape{
		"pane_id": typedShape("string"), "terminal_id": typedShape("string"), "workspace_id": typedShape("string"),
		"tab_id": typedShape("string"), "focused": typedShape("boolean"),
		"agent_status": refShape("#/schemas/success_response/$defs/AgentStatus"), "revision": typedShape("integer"),
		"agent": typedShape("string", "null"), "cwd": typedShape("string", "null"), "foreground_cwd": typedShape("string", "null"),
		"agent_session": anyOfShape(refShape("#/schemas/success_response/$defs/AgentSessionInfo"), typedShape("null")),
	})
}

func agentInfoShape() schemaShape {
	shape := paneInfoShape()
	shape.properties["name"] = typedShape("string", "null")
	shape.properties["interactive_ready"] = typedShape("boolean")
	shape.properties["launch_pending"] = typedShape("boolean")
	shape.properties["state_change_seq"] = typedShape("integer")
	return shape
}

func validateSchemaShape(node *schemaNode, expected schemaShape, path string) error {
	if node == nil {
		return fmt.Errorf("%s is missing", path)
	}
	if expected.ref != "" && node.Ref != expected.ref {
		return fmt.Errorf("%s ref = %q, want %q", path, node.Ref, expected.ref)
	}
	if expected.types != nil {
		types, err := decodeSchemaStrings(node.Type)
		if err != nil || !slices.Equal(types, expected.types) {
			return fmt.Errorf("%s type = %s, want %v", path, node.Type, expected.types)
		}
	}
	if expected.enum != nil {
		values, err := decodeRawStrings(node.Enum)
		if err != nil || !slices.Equal(values, expected.enum) {
			return fmt.Errorf("%s enum = %v, want %v", path, values, expected.enum)
		}
	}
	if expected.hasConstant {
		var constant string
		if err := json.Unmarshal(node.Const, &constant); err != nil || constant != expected.constant {
			return fmt.Errorf("%s const is incompatible", path)
		}
	}
	if expected.exactRequired {
		if !slices.Equal(node.Required, expected.required) {
			return fmt.Errorf("%s required = %v, want %v", path, node.Required, expected.required)
		}
	} else {
		for _, field := range expected.required {
			if !slices.Contains(node.Required, field) {
				return fmt.Errorf("%s missing required field %q", path, field)
			}
		}
	}
	for field, fieldShape := range expected.properties {
		if err := validateSchemaShape(node.Properties[field], fieldShape, path+"."+field); err != nil {
			return err
		}
	}
	if expected.items != nil {
		if err := validateSchemaShape(node.Items, *expected.items, path+".items"); err != nil {
			return err
		}
	}
	if expected.additionalProperties != nil {
		if err := validateSchemaShape(node.AdditionalProperties, *expected.additionalProperties, path+".additionalProperties"); err != nil {
			return err
		}
	}
	if err := validateAlternatives(node.OneOf, expected.oneOf, path+".oneOf"); err != nil {
		return err
	}
	return validateAlternatives(node.AnyOf, expected.anyOf, path+".anyOf")
}

func validateContractShape(document *capabilityDocument, node *schemaNode, expected schemaShape, path string) error {
	if err := validateSchemaShape(node, expected, path); err != nil {
		return err
	}
	return validateShapeReferences(document, node, expected, path)
}

func validateShapeReferences(document *capabilityDocument, node *schemaNode, expected schemaShape, path string) error {
	if expected.ref != "" {
		if _, err := resolveSchema(document, node); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	for field, fieldShape := range expected.properties {
		if err := validateShapeReferences(document, node.Properties[field], fieldShape, path+"."+field); err != nil {
			return err
		}
	}
	if expected.items != nil {
		if err := validateShapeReferences(document, node.Items, *expected.items, path+".items"); err != nil {
			return err
		}
	}
	if expected.additionalProperties != nil {
		if err := validateShapeReferences(document, node.AdditionalProperties, *expected.additionalProperties, path+".additionalProperties"); err != nil {
			return err
		}
	}
	for index := range expected.oneOf {
		if err := validateShapeReferences(document, node.OneOf[index], expected.oneOf[index], fmt.Sprintf("%s.oneOf[%d]", path, index)); err != nil {
			return err
		}
	}
	for index := range expected.anyOf {
		if err := validateShapeReferences(document, node.AnyOf[index], expected.anyOf[index], fmt.Sprintf("%s.anyOf[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateAlternatives(nodes []*schemaNode, expected []schemaShape, path string) error {
	if expected == nil {
		return nil
	}
	if len(nodes) != len(expected) {
		return fmt.Errorf("%s has %d alternatives, want %d", path, len(nodes), len(expected))
	}
	for index := range expected {
		if err := validateSchemaShape(nodes[index], expected[index], fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func decodeSchemaStrings(data json.RawMessage) ([]string, error) {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func decodeRawStrings(values []json.RawMessage) ([]string, error) {
	result := make([]string, len(values))
	for index := range values {
		if err := json.Unmarshal(values[index], &result[index]); err != nil {
			return nil, err
		}
	}
	return result, nil
}
