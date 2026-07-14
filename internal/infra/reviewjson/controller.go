package reviewjson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const controllerReadOnlySandbox = "read-only"

// ControllerContextAttestation identifies the authoritative Codex turn context
// that governs a native reviewer spawn.
type ControllerContextAttestation struct {
	TurnID        string
	ContextSHA256 string
	SandboxMode   string
	Timestamp     time.Time
}

// AttestController returns the latest authoritative turn context from the
// prepared parent controller rollout. It fails closed unless that context has
// a canonical turn UUID and an enforceable read-only sandbox.
func AttestController(
	sessionsRoot string,
	parentThreadID string,
) (ControllerContextAttestation, error) {
	if !isCanonicalUUID(parentThreadID) {
		return ControllerContextAttestation{}, unavailable(
			"parent thread ID is not a canonical UUID",
			nil,
		)
	}
	path, err := findUniqueRollout(sessionsRoot, parentThreadID)
	if err != nil {
		return ControllerContextAttestation{}, err
	}
	return readLatestControllerContext(path, parentThreadID)
}

type controllerTurnContextPayload struct {
	TurnID        json.RawMessage `json:"turn_id"`
	SandboxPolicy *struct {
		Type json.RawMessage `json:"type"`
	} `json:"sandbox_policy"`
	PermissionProfile       json.RawMessage `json:"permission_profile"`
	FileSystemSandboxPolicy json.RawMessage `json:"file_system_sandbox_policy"`
}

type controllerPermissionProfile struct {
	Type       json.RawMessage                 `json:"type"`
	FileSystem *controllerPermissionFileSystem `json:"file_system"`
}

type controllerPermissionFileSystem struct {
	Type    json.RawMessage `json:"type"`
	Entries json.RawMessage `json:"entries"`
}

type controllerFileSystemPolicy struct {
	Kind    json.RawMessage `json:"kind"`
	Entries json.RawMessage `json:"entries"`
}

type controllerPermissionEntry struct {
	Access json.RawMessage `json:"access"`
}

func parseControllerTurnContext(record rolloutRecord) (ControllerContextAttestation, error) {
	return parseControllerTurnContextRecord(record, true)
}

func parseControllerTurnContextRecord(
	record rolloutRecord,
	requireReadOnly bool,
) (ControllerContextAttestation, error) {
	timestampText, err := requiredJSONString(
		record.Timestamp,
		"controller turn_context timestamp",
	)
	if err != nil {
		return ControllerContextAttestation{}, err
	}
	timestamp, err := time.Parse(time.RFC3339Nano, timestampText)
	if err != nil {
		return ControllerContextAttestation{}, unavailable(
			"controller turn_context timestamp is not RFC3339",
			err,
		)
	}

	var payload controllerTurnContextPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return ControllerContextAttestation{}, unavailable(
			"malformed controller turn_context payload",
			err,
		)
	}
	turnID, err := requiredJSONString(payload.TurnID, "controller turn_context.turn_id")
	if err != nil {
		return ControllerContextAttestation{}, err
	}
	if !isCanonicalUUID(turnID) {
		return ControllerContextAttestation{}, mismatch(
			"controller turn_context.turn_id is not a canonical UUID",
		)
	}
	if payload.SandboxPolicy == nil {
		return ControllerContextAttestation{}, unavailable(
			"controller turn_context.sandbox_policy is missing",
			nil,
		)
	}
	sandboxMode, err := requiredJSONString(
		payload.SandboxPolicy.Type,
		"controller turn_context.sandbox_policy.type",
	)
	if err != nil {
		return ControllerContextAttestation{}, err
	}
	if requireReadOnly && sandboxMode != controllerReadOnlySandbox {
		return ControllerContextAttestation{}, mismatch(
			"controller turn_context sandbox is not read-only",
		)
	}
	if sandboxMode == controllerReadOnlySandbox {
		if err := validateControllerReadOnlyPermissions(payload); err != nil {
			return ControllerContextAttestation{}, err
		}
	}
	digest := sha256.Sum256(record.Payload)
	return ControllerContextAttestation{
		TurnID:        turnID,
		ContextSHA256: hex.EncodeToString(digest[:]),
		SandboxMode:   sandboxMode,
		Timestamp:     timestamp,
	}, nil
}

func validateControllerReadOnlyPermissions(payload controllerTurnContextPayload) error {
	// Older Codex rollouts expose only sandbox_policy. Preserve that format,
	// but validate every effective-permission representation when it is present.
	if len(payload.PermissionProfile) != 0 {
		if bytes.Equal(bytes.TrimSpace(payload.PermissionProfile), []byte("null")) {
			return unavailable(
				"controller turn_context.permission_profile is null",
				nil,
			)
		}
		var permissionProfile controllerPermissionProfile
		if err := json.Unmarshal(payload.PermissionProfile, &permissionProfile); err != nil {
			return unavailable(
				"controller turn_context.permission_profile is malformed",
				err,
			)
		}
		profileType, err := requiredJSONString(
			permissionProfile.Type,
			"controller turn_context.permission_profile.type",
		)
		if err != nil {
			return err
		}
		if profileType != "managed" {
			return mismatch(
				"controller turn_context permission_profile is not managed read-only",
			)
		}
		if permissionProfile.FileSystem == nil {
			return unavailable(
				"controller turn_context.permission_profile.file_system is missing",
				nil,
			)
		}
		fileSystemType, err := requiredJSONString(
			permissionProfile.FileSystem.Type,
			"controller turn_context.permission_profile.file_system.type",
		)
		if err != nil {
			return err
		}
		if fileSystemType != "restricted" {
			return mismatch(
				"controller turn_context permission_profile file system is not restricted",
			)
		}
		if err := validateControllerReadOnlyEntries(
			permissionProfile.FileSystem.Entries,
			"controller turn_context.permission_profile.file_system.entries",
		); err != nil {
			return err
		}
	}

	if len(payload.FileSystemSandboxPolicy) != 0 {
		if bytes.Equal(bytes.TrimSpace(payload.FileSystemSandboxPolicy), []byte("null")) {
			return unavailable(
				"controller turn_context.file_system_sandbox_policy is null",
				nil,
			)
		}
		var fileSystemSandboxPolicy controllerFileSystemPolicy
		if err := json.Unmarshal(payload.FileSystemSandboxPolicy, &fileSystemSandboxPolicy); err != nil {
			return unavailable(
				"controller turn_context.file_system_sandbox_policy is malformed",
				err,
			)
		}
		kind, err := requiredJSONString(
			fileSystemSandboxPolicy.Kind,
			"controller turn_context.file_system_sandbox_policy.kind",
		)
		if err != nil {
			return err
		}
		if kind != "restricted" {
			return mismatch(
				"controller turn_context file_system_sandbox_policy is not restricted",
			)
		}
		if err := validateControllerReadOnlyEntries(
			fileSystemSandboxPolicy.Entries,
			"controller turn_context.file_system_sandbox_policy.entries",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateControllerReadOnlyEntries(raw json.RawMessage, field string) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return unavailable(field+" is missing", nil)
	}
	var entries []controllerPermissionEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return unavailable(field+" is not an array", err)
	}
	for index, entry := range entries {
		access, err := requiredJSONString(
			entry.Access,
			fmt.Sprintf("%s[%d].access", field, index),
		)
		if err != nil {
			return err
		}
		if access != "read" {
			return mismatch(fmt.Sprintf(
				"%s[%d] grants non-read-only access %q",
				field,
				index,
				access,
			))
		}
	}
	return nil
}

func readLatestControllerContext(
	path string,
	parentThreadID string,
) (ControllerContextAttestation, error) {
	file, err := os.Open(path)
	if err != nil {
		return ControllerContextAttestation{}, unavailable("read parent rollout", err)
	}
	defer func() {
		// Parsing has consumed the file before return. A close failure on this
		// read-only descriptor cannot change the attested records.
		_ = file.Close()
	}()

	decoder := json.NewDecoder(file)
	parentMetaCount := 0
	var latest *rolloutRecord
	for recordNumber := 1; ; recordNumber++ {
		var record rolloutRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ControllerContextAttestation{}, unavailable(
				fmt.Sprintf("malformed parent rollout record %d", recordNumber),
				err,
			)
		}
		switch record.Type {
		case "session_meta":
			parentMetaCount++
			if err := validateControllerSessionMeta(record.Payload, parentThreadID); err != nil {
				return ControllerContextAttestation{}, err
			}
		case "turn_context":
			copy := record
			latest = &copy
		}
	}
	if parentMetaCount != 1 {
		return ControllerContextAttestation{}, unavailable(
			fmt.Sprintf("parent rollout contains %d session_meta records, want 1", parentMetaCount),
			nil,
		)
	}
	if latest == nil {
		return ControllerContextAttestation{}, unavailable(
			"parent rollout contains no controller turn_context records",
			nil,
		)
	}
	return parseControllerTurnContext(*latest)
}

func validateControllerSessionMeta(raw json.RawMessage, parentThreadID string) error {
	var payload struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return unavailable("malformed parent session_meta payload", err)
	}
	id, err := requiredJSONString(payload.ID, "parent session_meta.id")
	if err != nil {
		return err
	}
	if id != parentThreadID {
		return mismatch("parent session_meta.id does not match the prepared parent")
	}
	return nil
}
