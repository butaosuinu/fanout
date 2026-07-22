package herdrrun

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseAdmittedVersionFloorAndStableSemver(t *testing.T) {
	for _, version := range []string{"0.7.5", "0.7.6", "0.10.0", "1.0.0", "0.7.5+build.1"} {
		got, err := parseAdmittedVersion([]byte("herdr " + version + "\n"))
		if err != nil || got != version {
			t.Errorf("parseAdmittedVersion(%q) = %q, %v", version, got, err)
		}
	}
	for _, version := range []string{"0.7.4", "0.7.5-preview.1", "0.7", "v0.7.5", "00.7.5", "0.7.5 other"} {
		if _, err := parseAdmittedVersion([]byte("herdr " + version + "\n")); err == nil {
			t.Errorf("parseAdmittedVersion(%q) accepted an unsupported version", version)
		}
	}
}

func TestValidateCapabilitySchemaRejectsUsedMethodAndFieldRemoval(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*capabilityDocument)
		wantErr string
	}{
		{
			name: "method",
			mutate: func(document *capabilityDocument) {
				variant := testSchemaVariant(document.Schemas["request"], "method", "agent.prompt")
				variant.Properties["method"].Const = json.RawMessage(`"agent.future"`)
			},
			wantErr: "method agent.prompt",
		},
		{
			name: "request field",
			mutate: func(document *capabilityDocument) {
				delete(document.Schemas["request"].Defs["AgentPromptParams"].Properties, "text")
			},
			wantErr: "AgentPromptParams.text is missing",
		},
		{
			name: "active manifest method",
			mutate: func(document *capabilityDocument) {
				variant := testSchemaVariant(document.Schemas["request"], "method", "server.agent_manifests")
				variant.Properties["method"].Const = json.RawMessage(`"server.future"`)
			},
			wantErr: "method server.agent_manifests",
		},
		{
			name: "full wave method",
			mutate: func(document *capabilityDocument) {
				variant := testSchemaVariant(document.Schemas["request"], "method", "plugin.list")
				variant.Properties["method"].Const = json.RawMessage(`"plugin.future"`)
			},
			wantErr: "method plugin.list",
		},
		{
			name: "active manifest field",
			mutate: func(document *capabilityDocument) {
				delete(document.Schemas["success_response"].Defs["AgentManifestInfo"].Properties, "active_version")
			},
			wantErr: "AgentManifestInfo.active_version is missing",
		},
		{
			name: "snapshot identity field",
			mutate: func(document *capabilityDocument) {
				delete(document.Schemas["success_response"].Defs["PaneInfo"].Properties, "terminal_id")
			},
			wantErr: "PaneInfo.terminal_id is missing",
		},
		{
			name: "request field type",
			mutate: func(document *capabilityDocument) {
				document.Schemas["request"].Defs["AgentPromptParams"].Properties["text"].Type = json.RawMessage(`"boolean"`)
			},
			wantErr: "AgentPromptParams.text type",
		},
		{
			name: "request field ref",
			mutate: func(document *capabilityDocument) {
				document.Schemas["request"].Defs["PaneReadParams"].Properties["source"].Ref = "#/schemas/request/$defs/ReadFormat"
			},
			wantErr: "PaneReadParams.source ref",
		},
		{
			name: "request enum",
			mutate: func(document *capabilityDocument) {
				document.Schemas["request"].Defs["ReadSource"].Enum = []json.RawMessage{json.RawMessage(`"visible"`)}
			},
			wantErr: "ReadSource enum",
		},
		{
			name: "result const type",
			mutate: func(document *capabilityDocument) {
				result := document.Schemas["success_response"].Defs["ResponseResult"]
				variant := testSchemaVariant(result, "type", "agent_prompted")
				variant.Properties["type"].Type = json.RawMessage(`"integer"`)
			},
			wantErr: "result agent_prompted.type type",
		},
		{
			name: "success envelope required",
			mutate: func(document *capabilityDocument) {
				document.Schemas["success_response"].Required = []string{"result"}
			},
			wantErr: `missing required field "id"`,
		},
		{
			name: "protocol",
			mutate: func(document *capabilityDocument) {
				document.Protocol = 18
			},
			wantErr: "unsupported herdr API tuple",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document capabilityDocument
			if err := json.Unmarshal([]byte(validCapabilitySchema()), &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(&document)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			err = validateCapabilitySchema(data)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateCapabilitySchema() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func validCapabilitySchema() string {
	request := schemaNodeForTest(objectShape([]string{"id"}, false, map[string]schemaShape{"id": typedShape("string")}))
	request.Defs = map[string]*schemaNode{}
	for name, shape := range requestDefinitionShapes {
		request.Defs[name] = schemaNodeForTest(shape)
	}
	for _, requirement := range requiredMethodCapabilities {
		definition := requestMethodDefinitions[requirement.name]
		request.OneOf = append(request.OneOf, schemaNodeForTest(objectShape([]string{"method", "params"}, false, map[string]schemaShape{
			"method": constStringShape(requirement.name), "params": refShape("#/schemas/request/$defs/" + definition),
		})))
	}

	result := &schemaNode{}
	for _, shape := range successResultShapes {
		result.OneOf = append(result.OneOf, schemaNodeForTest(shape))
	}
	success := schemaNodeForTest(objectShape([]string{"id", "result"}, false, map[string]schemaShape{
		"id": typedShape("string"), "result": refShape("#/schemas/success_response/$defs/ResponseResult"),
	}))
	success.Defs = map[string]*schemaNode{"ResponseResult": result}
	for name, shape := range successDefinitionShapes {
		success.Defs[name] = schemaNodeForTest(shape)
	}
	success.Defs["EventEnvelope"] = schemaNodeForTest(objectShape(nil, false, nil))
	success.Defs["InstalledPluginInfo"] = schemaNodeForTest(objectShape(nil, false, nil))

	errorResponse := schemaNodeForTest(objectShape([]string{"id", "error"}, false, map[string]schemaShape{
		"id": typedShape("string"), "error": refShape("#/schemas/error_response/$defs/ErrorBody"),
	}))
	errorResponse.Defs = map[string]*schemaNode{
		"ErrorBody": schemaNodeForTest(objectShape([]string{"code", "message"}, false, map[string]schemaShape{
			"code": typedShape("string"), "message": typedShape("string"),
		})),
	}
	document := capabilityDocument{
		Protocol: supportedProtocol, SchemaVersion: supportedSchema,
		Schemas: map[string]*schemaNode{"request": request, "success_response": success, "error_response": errorResponse},
	}
	data, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
}

func schemaNodeForTest(shape schemaShape) *schemaNode {
	node := &schemaNode{Ref: shape.ref, Required: append([]string(nil), shape.required...)}
	if shape.types != nil {
		var value any = shape.types
		if len(shape.types) == 1 {
			value = shape.types[0]
		}
		node.Type, _ = json.Marshal(value)
	}
	if shape.hasConstant {
		node.Const, _ = json.Marshal(shape.constant)
	}
	for _, value := range shape.enum {
		encoded, _ := json.Marshal(value)
		node.Enum = append(node.Enum, encoded)
	}
	if shape.properties != nil {
		node.Properties = make(map[string]*schemaNode, len(shape.properties))
		for name, property := range shape.properties {
			node.Properties[name] = schemaNodeForTest(property)
		}
	}
	if shape.items != nil {
		node.Items = schemaNodeForTest(*shape.items)
	}
	if shape.additionalProperties != nil {
		node.AdditionalProperties = schemaNodeForTest(*shape.additionalProperties)
	}
	for _, alternative := range shape.oneOf {
		node.OneOf = append(node.OneOf, schemaNodeForTest(alternative))
	}
	for _, alternative := range shape.anyOf {
		node.AnyOf = append(node.AnyOf, schemaNodeForTest(alternative))
	}
	return node
}

func testSchemaVariant(node *schemaNode, property, value string) *schemaNode {
	for _, variant := range node.OneOf {
		var constant string
		if field := variant.Properties[property]; field != nil && json.Unmarshal(field.Const, &constant) == nil && constant == value {
			return variant
		}
	}
	panic(fmt.Sprintf("missing %s %q", property, value))
}
