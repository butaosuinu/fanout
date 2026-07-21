package herdrrun

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAdmittedVersionAcceptsStableFloorAndUpperVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"0.7.4", "0.8.0", "1.0.0", "0.7.4+release.1"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			got, err := parseAdmittedVersion([]byte("herdr " + version + "\n"))
			if err != nil {
				t.Fatalf("parseAdmittedVersion() error = %v", err)
			}
			if got != version {
				t.Fatalf("parseAdmittedVersion() = %q, want %q", got, version)
			}
		})
	}
}

func TestParseAdmittedVersionRejectsUnsupportedVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
	}{
		{name: "below floor", output: "herdr 0.7.3"},
		{name: "prerelease", output: "herdr 0.7.4-rc.1"},
		{name: "unparseable", output: "herdr newest"},
		{name: "missing product", output: "0.7.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseAdmittedVersion([]byte(tt.output))
			if err == nil || !strings.Contains(err.Error(), "stable >=0.7.4") {
				t.Fatalf("parseAdmittedVersion(%q) error = %v, want stable floor error", tt.output, err)
			}
		})
	}
}

func TestValidateCapabilitySchemaAcceptsRequiredStructure(t *testing.T) {
	t.Parallel()
	if err := validateCapabilitySchema(syntheticCapabilitySchema(t)); err != nil {
		t.Fatalf("validateCapabilitySchema() error = %v", err)
	}
}

func TestValidateCapabilitySchemaIgnoresUnrelatedAdditiveResultVariants(t *testing.T) {
	t.Parallel()
	document := syntheticCapabilityDocument()
	futureType := "future_plugin_result"
	document.Schemas["success_response"].Defs["ResponseResult"].OneOf = append(
		document.Schemas["success_response"].Defs["ResponseResult"].OneOf,
		&schemaNode{
			Types: schemaTypes{"object"},
			Properties: map[string]*schemaNode{
				"type":    {Types: schemaTypes{"string"}, Const: &futureType, ConstIsString: true},
				"payload": {Ref: "#/schemas/future/$defs/NotUsedByFanout"},
			},
		},
	)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCapabilitySchema(data); err != nil {
		t.Fatalf("validateCapabilitySchema() rejected unrelated additive result: %v", err)
	}
}

func TestValidateCapabilitySchemaIgnoresUnrelatedOptionalFields(t *testing.T) {
	t.Parallel()
	document := syntheticCapabilityDocument()
	requestVariant, err := findConstVariant(document.Schemas["request"].OneOf, "method", "pane.send_text")
	if err != nil {
		t.Fatal(err)
	}
	requestVariant.Properties["future_request_metadata"] = &schemaNode{Ref: "https://example.invalid/future-request-schema"}

	result := document.Schemas["success_response"].Defs["ResponseResult"]
	resultVariant, err := findConstVariant(result.OneOf, "type", "session_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	resultVariant.Properties["future_result_metadata"] = &schemaNode{Ref: "https://example.invalid/future-result-schema"}

	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCapabilitySchema(data); err != nil {
		t.Fatalf("validateCapabilitySchema() rejected unrelated optional fields: %v", err)
	}
}

func TestValidateCapabilitySchemaAcceptsUnrelatedAdditiveRequiredResponseFields(t *testing.T) {
	t.Parallel()
	document := syntheticCapabilityDocument()
	for _, name := range []string{"success_response", "error_response"} {
		envelope := document.Schemas[name]
		envelope.Required = append(envelope.Required, "future_trace_id")
		envelope.Properties["future_trace_id"] = &schemaNode{Types: schemaTypes{"string"}}
	}

	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCapabilitySchema(data); err != nil {
		t.Fatalf("validateCapabilitySchema() rejected additive required response fields: %v", err)
	}
}

func TestValidateCapabilitySchemaIgnoresUnrelatedJSONSchemaScalarKinds(t *testing.T) {
	t.Parallel()
	var raw map[string]any
	if err := json.Unmarshal(syntheticCapabilitySchema(t), &raw); err != nil {
		t.Fatal(err)
	}
	schemas := raw["schemas"].(map[string]any)
	request := schemas["request"].(map[string]any)
	request["oneOf"] = append(request["oneOf"].([]any), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"method":  map[string]any{"const": 42},
			"payload": map[string]any{"enum": []any{1, true, nil}},
		},
	})
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCapabilitySchema(data); err != nil {
		t.Fatalf("validateCapabilitySchema() rejected unrelated JSON Schema scalar kinds: %v", err)
	}
}

func TestRequiredMethodsExcludeCLIOnlySurfaces(t *testing.T) {
	t.Parallel()
	methods := make(map[string]bool)
	for _, method := range requiredMethods() {
		methods[method.name] = true
	}
	for _, cliOnly := range []string{"pane.read", "pane.close", "workspace.focus", "agent.focus"} {
		if methods[cliOnly] {
			t.Fatalf("requiredMethods() contains CLI-only or unused surface %q", cliOnly)
		}
	}
}

func TestValidateCapabilitySchemaFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*capabilityDocument)
		wantErr string
	}{
		{
			name: "protocol mismatch",
			mutate: func(document *capabilityDocument) {
				document.Protocol++
			},
			wantErr: "unsupported herdr API tuple",
		},
		{
			name: "schema version mismatch",
			mutate: func(document *capabilityDocument) {
				document.SchemaVersion++
			},
			wantErr: "unsupported herdr API tuple",
		},
		{
			name: "method missing",
			mutate: func(document *capabilityDocument) {
				request := document.Schemas["request"]
				for i, variant := range request.OneOf {
					if *variant.Properties["method"].Const == "pane.send_text" {
						request.OneOf = append(request.OneOf[:i], request.OneOf[i+1:]...)
						return
					}
				}
			},
			wantErr: `missing method="pane.send_text" variant`,
		},
		{
			name: "request field missing",
			mutate: func(document *capabilityDocument) {
				delete(document.Schemas["request"].Defs["PaneSendTextParams"].Properties, "text")
			},
			wantErr: `request.pane.send_text.params is missing property "text"`,
		},
		{
			name: "result identity field missing",
			mutate: func(document *capabilityDocument) {
				result := document.Schemas["success_response"].Defs["ResponseResult"]
				variant, err := findConstVariant(result.OneOf, "type", "session_snapshot")
				if err != nil {
					t.Fatal(err)
				}
				snapshot := variant.Properties["snapshot"]
				pane := snapshot.Properties["panes"].Items
				delete(pane.Properties, "terminal_id")
			},
			wantErr: `result.session_snapshot.snapshot.panes[] is missing property "terminal_id"`,
		},
		{
			name: "layout rectangle field missing",
			mutate: func(document *capabilityDocument) {
				result := document.Schemas["success_response"].Defs["ResponseResult"]
				variant, err := findConstVariant(result.OneOf, "type", "session_snapshot")
				if err != nil {
					t.Fatal(err)
				}
				snapshot := variant.Properties["snapshot"]
				layout := snapshot.Properties["layouts"].Items
				delete(layout.Properties["area"].Properties, "width")
			},
			wantErr: `result.session_snapshot.snapshot.layouts[].area is missing property "width"`,
		},
		{
			name: "process argv items missing",
			mutate: func(document *capabilityDocument) {
				argv := syntheticProcessArgvSchema(t, document)
				argv.Items = nil
			},
			wantErr: `result.pane_process_info.process_info.foreground_processes[].argv is missing items`,
		},
		{
			name: "process argv items are not strings",
			mutate: func(document *capabilityDocument) {
				argv := syntheticProcessArgvSchema(t, document)
				argv.Items = &schemaNode{Types: schemaTypes{"integer"}}
			},
			wantErr: `result.pane_process_info.process_info.foreground_processes[].argv[] type=[integer], want [string]`,
		},
		{
			name: "unresolvable reference",
			mutate: func(document *capabilityDocument) {
				document.Schemas["request"].OneOf[0].Properties["params"].Ref = "#/schemas/request/$defs/Missing"
			},
			wantErr: "unresolvable schema reference",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := syntheticCapabilityDocument()
			tt.mutate(document)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			err = validateCapabilitySchema(data)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCapabilitySchema() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func syntheticProcessArgvSchema(t *testing.T, document *capabilityDocument) *schemaNode {
	t.Helper()
	result := document.Schemas["success_response"].Defs["ResponseResult"]
	variant, err := findConstVariant(result.OneOf, "type", "pane_process_info")
	if err != nil {
		t.Fatal(err)
	}
	return variant.Properties["process_info"].Properties["foreground_processes"].Items.Properties["argv"]
}

func syntheticCapabilitySchema(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(syntheticCapabilityDocument())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func syntheticCapabilityDocument() *capabilityDocument {
	requestDefs := make(map[string]*schemaNode)
	requestVariants := make([]*schemaNode, 0, len(requiredMethods()))
	for _, method := range requiredMethods() {
		definition := syntheticDefinitionName(method.name)
		requestDefs[definition] = expectationNode(method.params)
		methodName := method.name
		requestVariants = append(requestVariants, &schemaNode{
			Types:    schemaTypes{"object"},
			Required: []string{"method", "params"},
			Properties: map[string]*schemaNode{
				"method": {Types: schemaTypes{"string"}, Const: &methodName, ConstIsString: true},
				"params": {Ref: "#/schemas/request/$defs/" + definition},
			},
		})
	}

	resultVariants := make([]*schemaNode, 0, len(requiredResultVariants()))
	for _, expected := range requiredResultVariants() {
		resultVariants = append(resultVariants, expectationNode(expected))
	}
	errorBody := expectationNode(objectShape(
		[]string{"code", "message"},
		false,
		map[string]*schemaExpectation{"code": typed("string"), "message": typed("string")},
	))

	return &capabilityDocument{
		Protocol:      supportedProtocol,
		SchemaVersion: supportedSchema,
		Schemas: map[string]*schemaNode{
			"request": {
				Types:    schemaTypes{"object"},
				Required: []string{"id"},
				Properties: map[string]*schemaNode{
					"id": {Types: schemaTypes{"string"}},
				},
				OneOf: requestVariants,
				Defs:  requestDefs,
			},
			"success_response": {
				Types:    schemaTypes{"object"},
				Required: []string{"id", "result"},
				Properties: map[string]*schemaNode{
					"id":     {Types: schemaTypes{"string"}},
					"result": {Ref: "#/schemas/success_response/$defs/ResponseResult"},
				},
				Defs: map[string]*schemaNode{
					"ResponseResult": {OneOf: resultVariants},
				},
			},
			"error_response": {
				Types:    schemaTypes{"object"},
				Required: []string{"id", "error"},
				Properties: map[string]*schemaNode{
					"id":    {Types: schemaTypes{"string"}},
					"error": {Ref: "#/schemas/error_response/$defs/ErrorBody"},
				},
				Defs: map[string]*schemaNode{"ErrorBody": errorBody},
			},
		},
	}
}

func syntheticDefinitionName(method string) string {
	var builder strings.Builder
	upper := true
	for _, char := range method {
		if char == '.' || char == '_' {
			upper = true
			continue
		}
		if upper {
			builder.WriteString(strings.ToUpper(string(char)))
			upper = false
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String() + "Params"
}

func expectationNode(expected *schemaExpectation) *schemaNode {
	node := &schemaNode{
		Types:         schemaTypes(append([]string(nil), expected.Types...)),
		Enum:          append([]string(nil), expected.Enum...),
		EnumIsStrings: expected.Enum != nil,
		Required:      append([]string(nil), expected.Required...),
	}
	if expected.Const != "" {
		value := expected.Const
		node.Const = &value
		node.ConstIsString = true
	}
	if len(expected.Properties) > 0 {
		node.Properties = make(map[string]*schemaNode, len(expected.Properties))
		for name, property := range expected.Properties {
			node.Properties[name] = expectationNode(property)
		}
	}
	for _, branch := range expected.AnyOf {
		node.AnyOf = append(node.AnyOf, expectationNode(branch))
	}
	if expected.Items != nil {
		node.Items = expectationNode(expected.Items)
	}
	if expected.AdditionalProperties != nil {
		node.AdditionalProperties = expectationNode(expected.AdditionalProperties)
	}
	return node
}
