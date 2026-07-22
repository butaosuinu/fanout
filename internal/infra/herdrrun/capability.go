package herdrrun

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	Ref                  string                 `json:"$ref"`
	Type                 json.RawMessage        `json:"type"`
	Const                json.RawMessage        `json:"const"`
	Enum                 []json.RawMessage      `json:"enum"`
	OneOf                []*schemaNode          `json:"oneOf"`
	AnyOf                []*schemaNode          `json:"anyOf"`
	Properties           map[string]*schemaNode `json:"properties"`
	Required             []string               `json:"required"`
	Defs                 map[string]*schemaNode `json:"$defs"`
	Items                *schemaNode            `json:"items"`
	AdditionalProperties *schemaNode            `json:"additionalProperties"`
	Boolean              *bool                  `json:"-"`
}

func (n *schemaNode) UnmarshalJSON(data []byte) error {
	var boolean bool
	if err := json.Unmarshal(data, &boolean); err == nil {
		*n = schemaNode{Boolean: &boolean}
		return nil
	}
	type plainSchemaNode schemaNode
	var decoded plainSchemaNode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = schemaNode(decoded)
	return nil
}

type methodRequirement struct {
	name string
}

var requiredMethodCapabilities = []methodRequirement{
	{name: "server.stop"},
	{name: "server.agent_manifests"},
	{name: "session.snapshot"},
	{name: "workspace.create"},
	{name: "workspace.focus"},
	{name: "workspace.report_metadata"},
	{name: "workspace.close"},
	{name: "worktree.list"},
	{name: "worktree.create"},
	{name: "worktree.open"},
	{name: "worktree.remove"},
	{name: "agent.list"},
	{name: "agent.read"},
	{name: "agent.rename"},
	{name: "agent.focus"},
	{name: "agent.prompt"},
	{name: "agent.wait"},
	{name: "pane.get"},
	{name: "pane.process_info"},
	{name: "pane.read"},
	{name: "pane.send_input"},
	{name: "pane.report_metadata"},
	{name: "pane.close"},
	{name: "pane.wait_for_output"},
	{name: "plugin.list"},
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
	requestEnvelope := objectShape([]string{"id"}, false, map[string]schemaShape{"id": typedShape("string")})
	if err := validateContractShape(&document, request, requestEnvelope, "schemas.request"); err != nil {
		return fmt.Errorf("unsupported herdr request schema: %w", err)
	}
	for _, requirement := range requiredMethodCapabilities {
		variant, err := findVariant(&document, request, "method", requirement.name)
		if err != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s: %w", requirement.name, err)
		}
		definition := requestMethodDefinitions[requirement.name]
		variantShape := objectShape([]string{"method", "params"}, false, map[string]schemaShape{
			"method": constStringShape(requirement.name),
			"params": refShape("#/schemas/request/$defs/" + definition),
		})
		if shapeErr := validateContractShape(&document, variant, variantShape, "method "+requirement.name); shapeErr != nil {
			return fmt.Errorf("unsupported herdr request schema: %w", shapeErr)
		}
		params, err := resolveSchema(&document, variant.Properties["params"])
		if err != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s params: %w", requirement.name, err)
		}
		if shapeErr := validateContractShape(&document, params, requestDefinitionShapes[definition], "request.$defs."+definition); shapeErr != nil {
			return fmt.Errorf("unsupported herdr request schema: method %s params: %w", requirement.name, shapeErr)
		}
	}
	for name, shape := range requestDefinitionShapes {
		node := request.Defs[name]
		if node == nil {
			return fmt.Errorf("unsupported herdr request schema: missing request.$defs.%s", name)
		}
		if err := validateContractShape(&document, node, shape, "request.$defs."+name); err != nil {
			return fmt.Errorf("unsupported herdr request schema: %w", err)
		}
	}

	success := document.Schemas["success_response"]
	if success == nil {
		return fmt.Errorf("unsupported herdr response schema: missing schemas.success_response")
	}
	successEnvelope := objectShape([]string{"id", "result"}, false, map[string]schemaShape{
		"id": typedShape("string"), "result": refShape("#/schemas/success_response/$defs/ResponseResult"),
	})
	if err := validateContractShape(&document, success, successEnvelope, "schemas.success_response"); err != nil {
		return fmt.Errorf("unsupported herdr response schema: %w", err)
	}
	errorResponse := document.Schemas["error_response"]
	if errorResponse == nil {
		return fmt.Errorf("unsupported herdr response schema: missing schemas.error_response")
	}
	errorEnvelope := objectShape([]string{"id", "error"}, false, map[string]schemaShape{
		"id": typedShape("string"), "error": refShape("#/schemas/error_response/$defs/ErrorBody"),
	})
	if err := validateContractShape(&document, errorResponse, errorEnvelope, "schemas.error_response"); err != nil {
		return fmt.Errorf("unsupported herdr response schema: %w", err)
	}
	errorBody := objectShape([]string{"code", "message"}, false, map[string]schemaShape{
		"code": typedShape("string"), "message": typedShape("string"),
	})
	if err := validateContractShape(&document, errorResponse.Defs["ErrorBody"], errorBody, "error_response.$defs.ErrorBody"); err != nil {
		return fmt.Errorf("unsupported herdr response schema: %w", err)
	}
	result, err := resolveSchema(&document, success.Properties["result"])
	if err != nil {
		return fmt.Errorf("unsupported herdr response schema: result: %w", err)
	}
	for name, shape := range successResultShapes {
		variant, findErr := findVariant(&document, result, "type", name)
		if findErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, findErr)
		}
		if shapeErr := validateContractShape(&document, variant, shape, "result "+name); shapeErr != nil {
			return fmt.Errorf("unsupported herdr response schema: result %s: %w", name, shapeErr)
		}
	}
	for name, shape := range successDefinitionShapes {
		node := success.Defs[name]
		if node == nil {
			return fmt.Errorf("unsupported herdr response schema: missing %s", name)
		}
		if err := validateContractShape(&document, node, shape, "success_response.$defs."+name); err != nil {
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
