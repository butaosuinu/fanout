package reviewjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const (
	// AttestationVersion identifies the rollout metadata contract consumed by
	// the post-work-review driver.
	AttestationVersion = "4"

	attestedNoHistoryMode = "no-history"

	attestationValidFile = "attestation_valid"
)

var attestationCacheFiles = []string{
	"attestation_version",
	"attested_session_id",
	"attested_parent_thread_id",
	"attested_agent_role",
	"attested_model",
	"attested_sandbox_mode",
	"attested_approval_policy",
	"attested_history_mode",
	"attested_reviewer_spawn_calls",
	"attested_controller_turn_id",
	"attested_controller_context_sha256",
	"attested_controller_sandbox_mode",
	"attested_spawn_authorized_at",
	"attestation_error",
	"attestation_error_kind",
	attestationValidFile,
}

// AttestationErrorKind separates missing evidence, contradictory evidence, and
// a verified session identifier already consumed by another review call.
type AttestationErrorKind string

const (
	AttestationUnavailable AttestationErrorKind = "unavailable"
	AttestationMismatch    AttestationErrorKind = "mismatch"
	AttestationReused      AttestationErrorKind = "reused"
)

// AttestationError is returned whenever a reviewer rollout cannot be trusted.
type AttestationError struct {
	Kind   AttestationErrorKind
	detail string
	cause  error
}

func (e *AttestationError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("attestation %s: %s: %v", e.Kind, e.detail, e.cause)
	}
	return fmt.Sprintf("attestation %s: %s", e.Kind, e.detail)
}

func (e *AttestationError) Unwrap() error {
	return e.cause
}

// Attest projects resultPath and verifies that it is the exact output of one
// fresh native custom-agent rollout. Attestation cache files are written only
// from rollout metadata; attestation_valid is written last.
func Attest(
	resultPath string,
	outputDir string,
	sessionsRoot string,
	parentThreadID string,
	preparedAt string,
	agentConfigPath string,
	expectedBundlePath string,
	expectedControllerTurnID string,
	expectedControllerContextSHA256 string,
	spawnAuthorizedAt string,
	usedSessionIDsPath string,
) error {
	if err := clearAttestationCache(outputDir); err != nil {
		return unavailable("clear stale attestation cache", err)
	}

	attestation, err := buildAttestation(
		resultPath,
		outputDir,
		sessionsRoot,
		parentThreadID,
		preparedAt,
		agentConfigPath,
		expectedBundlePath,
		expectedControllerTurnID,
		expectedControllerContextSHA256,
		spawnAuthorizedAt,
		usedSessionIDsPath,
	)
	if err != nil {
		attestationErr := asAttestationError(err)
		if writeErr := writeAttestationFailure(outputDir, attestationErr); writeErr != nil {
			return unavailable("record attestation failure", errors.Join(attestationErr, writeErr))
		}
		return attestationErr
	}

	if err := writeAttestationSuccess(outputDir, attestation); err != nil {
		attestationErr := unavailable("write attestation cache", err)
		if writeErr := writeAttestationFailure(outputDir, attestationErr); writeErr != nil {
			return unavailable("record attestation write failure", errors.Join(attestationErr, writeErr))
		}
		return attestationErr
	}
	return nil
}

type attestation struct {
	sessionID               string
	parentThreadID          string
	agentRole               string
	model                   string
	sandboxMode             string
	approvalPolicy          string
	historyMode             string
	reviewerSpawns          int
	controllerTurnID        string
	controllerContextSHA256 string
	controllerSandboxMode   string
	spawnAuthorizedAt       string
}

func buildAttestation(
	resultPath string,
	outputDir string,
	sessionsRoot string,
	parentThreadID string,
	preparedAt string,
	agentConfigPath string,
	expectedBundlePath string,
	expectedControllerTurnID string,
	expectedControllerContextSHA256 string,
	spawnAuthorizedAt string,
	usedSessionIDsPath string,
) (attestation, error) {
	resultData, resultReadErr := os.ReadFile(resultPath)
	if resultReadErr != nil {
		return attestation{}, unavailable("read reviewer JSON", resultReadErr)
	}
	if projectErr := project(resultData, outputDir); projectErr != nil {
		return attestation{}, unavailable("project reviewer JSON", projectErr)
	}
	if !isCanonicalUUID(parentThreadID) {
		return attestation{}, unavailable("parent thread ID is not a canonical UUID", nil)
	}
	if expectedBundlePath == "" || !filepath.IsAbs(expectedBundlePath) {
		return attestation{}, unavailable("expected review bundle path is not absolute", nil)
	}
	bundleInfo, bundleInfoErr := os.Lstat(expectedBundlePath)
	if bundleInfoErr != nil {
		return attestation{}, unavailable("inspect expected review bundle", bundleInfoErr)
	}
	if bundleInfo.Mode()&os.ModeSymlink != 0 || !bundleInfo.Mode().IsRegular() {
		return attestation{}, unavailable("expected review bundle is not a regular file", nil)
	}
	preparedTime, parseErr := time.Parse(time.RFC3339Nano, preparedAt)
	if parseErr != nil {
		return attestation{}, unavailable("prepared_at is not RFC3339", parseErr)
	}
	if !isCanonicalUUID(expectedControllerTurnID) {
		return attestation{}, unavailable(
			"expected controller turn ID is not a canonical UUID",
			nil,
		)
	}
	if len(expectedControllerContextSHA256) != 64 ||
		strings.Trim(expectedControllerContextSHA256, "0123456789abcdef") != "" {
		return attestation{}, unavailable(
			"expected controller context SHA-256 is not lowercase hexadecimal",
			nil,
		)
	}
	spawnAuthorizedTime, parseErr := time.Parse(time.RFC3339Nano, spawnAuthorizedAt)
	if parseErr != nil {
		return attestation{}, unavailable("spawn_authorized_at is not RFC3339", parseErr)
	}

	config, configErr := readAgentConfig(agentConfigPath)
	if configErr != nil {
		return attestation{}, unavailable("read agent config", configErr)
	}
	if config.name != "post-work-reviewer" && config.name != "post-work-verifier" {
		return attestation{}, unavailable("agent config name is not a post-work-review role", nil)
	}
	if config.sandboxMode != "read-only" {
		return attestation{}, unavailable("agent config sandbox_mode is not read-only", nil)
	}
	if config.approvalPolicy != "never" {
		return attestation{}, unavailable("agent config approval_policy is not never", nil)
	}

	sessionID, sessionIDErr := reviewerSessionID(resultData)
	if sessionIDErr != nil {
		return attestation{}, sessionIDErr
	}
	if !isCanonicalUUID(sessionID) {
		return attestation{}, mismatch("reviewer_session_id is not a canonical UUID")
	}
	if sessionID == parentThreadID {
		return attestation{}, mismatch("reviewer session ID equals the parent thread ID")
	}
	if usedSessionIDsPath != "" {
		used, usedSessionIDsErr := readUsedSessionIDs(usedSessionIDsPath)
		if usedSessionIDsErr != nil {
			return attestation{}, unavailable("read used reviewer session IDs", usedSessionIDsErr)
		}
		if _, exists := used[sessionID]; exists {
			return attestation{}, reused("reviewer session UUID was already used")
		}
	}

	rolloutPath, rolloutPathErr := findUniqueRollout(sessionsRoot, sessionID)
	if rolloutPathErr != nil {
		return attestation{}, rolloutPathErr
	}
	metadata, rolloutErr := readRollout(rolloutPath)
	if rolloutErr != nil {
		return attestation{}, rolloutErr
	}
	freshAfter := preparedTime
	// Legacy v1 broad states stored only a second-resolution timestamp captured
	// before the bundle write. New states store a nanosecond timestamp after the
	// write, and verifier bundles reuse one path across rounds, so their current
	// mtime must not invalidate an older stored result.
	if !strings.Contains(preparedAt, ".") {
		if !bundleInfo.ModTime().After(preparedTime) {
			return attestation{}, unavailable(
				"legacy bundle mtime does not prove completion after prepared_at",
				nil,
			)
		}
		freshAfter = bundleInfo.ModTime()
	}
	if validateErr := validateRollout(
		metadata,
		resultData,
		sessionID,
		parentThreadID,
		freshAfter,
		config,
		expectedBundlePath,
	); validateErr != nil {
		return attestation{}, validateErr
	}
	parentRolloutPath, parentRolloutPathErr := findUniqueRollout(sessionsRoot, parentThreadID)
	if parentRolloutPathErr != nil {
		return attestation{}, parentRolloutPathErr
	}
	reviewerSpawns := 0
	var controllerContext ControllerContextAttestation
	if spawnErr := validateParentSpawn(
		parentRolloutPath,
		metadata,
		parentThreadID,
		config.name,
		expectedBundlePath,
		preparedTime,
		freshAfter,
		expectedControllerTurnID,
		expectedControllerContextSHA256,
		spawnAuthorizedTime,
		&reviewerSpawns,
		&controllerContext,
	); spawnErr != nil {
		return attestation{}, spawnErr
	}

	return attestation{
		sessionID:               metadata.sessionID,
		parentThreadID:          metadata.parentThreadID,
		agentRole:               metadata.agentRole,
		model:                   config.model,
		sandboxMode:             config.sandboxMode,
		approvalPolicy:          config.approvalPolicy,
		historyMode:             attestedNoHistoryMode,
		reviewerSpawns:          reviewerSpawns,
		controllerTurnID:        controllerContext.TurnID,
		controllerContextSHA256: controllerContext.ContextSHA256,
		controllerSandboxMode:   controllerContext.SandboxMode,
		spawnAuthorizedAt:       spawnAuthorizedAt,
	}, nil
}

func reviewerSessionID(data []byte) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return "", unavailable("reviewer JSON is not an object", err)
	}
	raw, ok := root["reviewer_session_id"]
	if !ok {
		return "", unavailable("reviewer JSON is missing reviewer_session_id", nil)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", unavailable("reviewer_session_id is not a string", err)
	}
	return value, nil
}

type agentConfig struct {
	name            string
	model           string
	reasoningEffort string
	sandboxMode     string
	approvalPolicy  string
}

func readAgentConfig(path string) (agentConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return agentConfig{}, err
	}
	defer func() {
		// The file is read-only and the parse result is already available; a
		// close error cannot make that result less trustworthy.
		_ = file.Close()
	}()

	wanted := map[string]*string{
		"name":                   nil,
		"model":                  nil,
		"model_reasoning_effort": nil,
		"sandbox_mode":           nil,
		"approval_policy":        nil,
	}
	seen := make(map[string]bool, len(wanted))
	topLevel := true
	multilineDelimiter := ""
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			lineNumber++
			if !utf8.ValidString(line) {
				return agentConfig{}, fmt.Errorf("line %d is not valid UTF-8", lineNumber)
			}
			if multilineDelimiter != "" {
				if strings.Contains(line, multilineDelimiter) {
					multilineDelimiter = ""
				}
			} else {
				rawTrimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(rawTrimmed, "#") {
					if _, value, found := strings.Cut(rawTrimmed, "="); found {
						rawValue := strings.TrimSpace(value)
						switch {
						case strings.HasPrefix(rawValue, `"""`):
							if strings.Count(rawValue, `"""`) == 1 {
								multilineDelimiter = `"""`
							}
							continue
						case strings.HasPrefix(rawValue, `'''`):
							if strings.Count(rawValue, `'''`) == 1 {
								multilineDelimiter = `'''`
							}
							continue
						}
					}
				}
				cleaned, cleanErr := stripTOMLComment(line)
				if cleanErr != nil {
					return agentConfig{}, fmt.Errorf("line %d: %w", lineNumber, cleanErr)
				}
				trimmed := strings.TrimSpace(cleaned)
				switch {
				case trimmed == "":
				case strings.HasPrefix(trimmed, "["):
					if !strings.HasSuffix(trimmed, "]") {
						return agentConfig{}, fmt.Errorf("line %d has an unparseable table header", lineNumber)
					}
					topLevel = false
				default:
					key, rawValue, splitErr := splitTOMLAssignment(trimmed)
					if splitErr != nil {
						return agentConfig{}, fmt.Errorf("line %d: %w", lineNumber, splitErr)
					}
					if strings.HasPrefix(rawValue, `"""`) {
						if strings.Count(rawValue, `"""`) == 1 {
							multilineDelimiter = `"""`
						}
						continue
					}
					if strings.HasPrefix(rawValue, `'''`) {
						if strings.Count(rawValue, `'''`) == 1 {
							multilineDelimiter = `'''`
						}
						continue
					}
					if !topLevel {
						continue
					}
					if _, ok := wanted[key]; !ok {
						continue
					}
					if seen[key] {
						return agentConfig{}, fmt.Errorf("duplicate top-level %s", key)
					}
					value, valueErr := parseSimpleTOMLString(rawValue)
					if valueErr != nil {
						return agentConfig{}, fmt.Errorf("top-level %s: %w", key, valueErr)
					}
					seen[key] = true
					valueCopy := value
					wanted[key] = &valueCopy
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return agentConfig{}, readErr
			}
			break
		}
	}
	if multilineDelimiter != "" {
		return agentConfig{}, errors.New("unterminated multiline string")
	}
	for _, key := range []string{
		"name",
		"model",
		"model_reasoning_effort",
		"sandbox_mode",
		"approval_policy",
	} {
		if wanted[key] == nil {
			return agentConfig{}, fmt.Errorf("missing top-level %s", key)
		}
	}
	return agentConfig{
		name:            *wanted["name"],
		model:           *wanted["model"],
		reasoningEffort: *wanted["model_reasoning_effort"],
		sandboxMode:     *wanted["sandbox_mode"],
		approvalPolicy:  *wanted["approval_policy"],
	}, nil
}

func stripTOMLComment(line string) (string, error) {
	inBasicString := false
	inLiteralString := false
	escaped := false
	for i, r := range line {
		switch {
		case inBasicString:
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inBasicString = false
			}
		case inLiteralString:
			if r == '\'' {
				inLiteralString = false
			}
		case r == '"':
			inBasicString = true
		case r == '\'':
			inLiteralString = true
		case r == '#':
			return line[:i], nil
		}
	}
	if inBasicString || inLiteralString || escaped {
		return "", errors.New("unterminated string")
	}
	return line, nil
}

func splitTOMLAssignment(line string) (string, string, error) {
	separator := strings.IndexByte(line, '=')
	if separator < 1 {
		return "", "", errors.New("expected key = value")
	}
	key := strings.TrimSpace(line[:separator])
	value := strings.TrimSpace(line[separator+1:])
	if key == "" || value == "" {
		return "", "", errors.New("expected non-empty key and value")
	}
	return key, value, nil
}

func parseSimpleTOMLString(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", errors.New("expected a basic quoted string")
	}
	value := raw[1 : len(raw)-1]
	if value == "" {
		return "", errors.New("value is empty")
	}
	if strings.ContainsAny(value, "\\\"\r\n") || !utf8.ValidString(value) {
		return "", errors.New("escaped, multiline, or invalid UTF-8 values are unsupported")
	}
	return value, nil
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range []byte(value) {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return false
			}
		}
	}
	return true
}

func readUsedSessionIDs(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errors.New("used session IDs file is not valid UTF-8")
	}
	used := make(map[string]struct{})
	lines := strings.Split(string(data), "\n")
	for index, line := range lines {
		if line == "" && index == len(lines)-1 {
			continue
		}
		if !isCanonicalUUID(line) {
			return nil, fmt.Errorf("line %d is not a canonical UUID", index+1)
		}
		used[line] = struct{}{}
	}
	return used, nil
}

func findUniqueRollout(sessionsRoot, sessionID string) (string, error) {
	var matches []string
	err := filepath.WalkDir(sessionsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionID+".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", unavailable("scan Codex sessions root", err)
	}
	switch len(matches) {
	case 0:
		return "", unavailable("rollout not found for reviewer session UUID", nil)
	case 1:
		return matches[0], nil
	default:
		return "", unavailable(
			fmt.Sprintf("rollout is ambiguous for reviewer session UUID (%d matches)", len(matches)),
			nil,
		)
	}
}

type rolloutMetadata struct {
	sessionID            string
	parentThreadID       string
	spawnParentThreadID  string
	threadSource         string
	agentRole            string
	agentPath            string
	multiAgentVersion    string
	forkedFromID         string
	sessionCreatedAt     time.Time
	turnContexts         []rolloutTurnContext
	userInputs           []rolloutInput
	agentInputs          []rolloutInput
	encryptedAgentInputs int
	taskCompleteCount    int
	lastAgentMessage     string
	lastAgentMessageSeen bool
}

type rolloutTurnContext struct {
	model           string
	reasoningEffort string
	sandboxMode     string
	approvalPolicy  string
}

type rolloutInput struct {
	texts     []string
	author    string
	recipient string
}

type rolloutRecord struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func readRollout(path string) (rolloutMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return rolloutMetadata{}, unavailable("read rollout", err)
	}
	defer func() {
		// Parsing has consumed the file before return. There is no recovery path
		// for a close failure on a read-only descriptor.
		_ = file.Close()
	}()

	decoder := json.NewDecoder(file)
	var metadata rolloutMetadata
	sessionMetaCount := 0
	for recordNumber := 1; ; recordNumber++ {
		var record rolloutRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return rolloutMetadata{}, unavailable(
				fmt.Sprintf("malformed rollout record %d", recordNumber),
				err,
			)
		}
		switch record.Type {
		case "session_meta":
			sessionMetaCount++
			parsed, err := parseSessionMeta(record.Payload)
			if err != nil {
				return rolloutMetadata{}, err
			}
			metadata.sessionID = parsed.sessionID
			metadata.parentThreadID = parsed.parentThreadID
			metadata.spawnParentThreadID = parsed.spawnParentThreadID
			metadata.threadSource = parsed.threadSource
			metadata.agentRole = parsed.agentRole
			metadata.agentPath = parsed.agentPath
			metadata.multiAgentVersion = parsed.multiAgentVersion
			metadata.forkedFromID = parsed.forkedFromID
			metadata.sessionCreatedAt = parsed.sessionCreatedAt
		case "turn_context":
			context, err := parseTurnContext(record.Payload)
			if err != nil {
				return rolloutMetadata{}, err
			}
			metadata.turnContexts = append(metadata.turnContexts, context)
		case "response_item":
			sandboxOverride, err := parseSandboxOverrideRequest(record.Payload)
			if err != nil {
				return rolloutMetadata{}, err
			}
			if sandboxOverride {
				return rolloutMetadata{}, mismatch(
					"reviewer rollout requested a sandbox permission override",
				)
			}
			userInput, agentInput, encryptedAgentInput, err := parseRolloutInput(record.Payload)
			if err != nil {
				return rolloutMetadata{}, err
			}
			if userInput != nil {
				metadata.userInputs = append(metadata.userInputs, *userInput)
			}
			if agentInput != nil {
				metadata.agentInputs = append(metadata.agentInputs, *agentInput)
			}
			if encryptedAgentInput {
				metadata.encryptedAgentInputs++
			}
		case "event_msg":
			complete, message, messageSeen, err := parseTaskComplete(record.Payload)
			if err != nil {
				return rolloutMetadata{}, err
			}
			if complete {
				metadata.taskCompleteCount++
				metadata.lastAgentMessage = message
				metadata.lastAgentMessageSeen = messageSeen
			}
		}
	}
	if sessionMetaCount != 1 {
		return rolloutMetadata{}, unavailable(
			fmt.Sprintf("rollout contains %d session_meta records, want 1", sessionMetaCount),
			nil,
		)
	}
	return metadata, nil
}

type sessionMetaPayload struct {
	ID                json.RawMessage `json:"id"`
	ParentThreadID    json.RawMessage `json:"parent_thread_id"`
	ForkedFromID      json.RawMessage `json:"forked_from_id"`
	Timestamp         json.RawMessage `json:"timestamp"`
	ThreadSource      json.RawMessage `json:"thread_source"`
	AgentRole         json.RawMessage `json:"agent_role"`
	AgentPath         json.RawMessage `json:"agent_path"`
	MultiAgentVersion json.RawMessage `json:"multi_agent_version"`
	Source            *struct {
		Subagent *struct {
			ThreadSpawn *struct {
				ParentThreadID json.RawMessage `json:"parent_thread_id"`
				AgentRole      json.RawMessage `json:"agent_role"`
				AgentPath      json.RawMessage `json:"agent_path"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	} `json:"source"`
}

func parseSessionMeta(raw json.RawMessage) (rolloutMetadata, error) {
	var payload sessionMetaPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return rolloutMetadata{}, unavailable("malformed session_meta payload", err)
	}
	id, err := requiredJSONString(payload.ID, "session_meta.id")
	if err != nil {
		return rolloutMetadata{}, err
	}
	parentID, err := requiredJSONString(payload.ParentThreadID, "session_meta.parent_thread_id")
	if err != nil {
		return rolloutMetadata{}, err
	}
	timestamp, err := requiredJSONString(payload.Timestamp, "session_meta.timestamp")
	if err != nil {
		return rolloutMetadata{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return rolloutMetadata{}, unavailable("session_meta.timestamp is not RFC3339", err)
	}
	threadSource, err := requiredJSONString(payload.ThreadSource, "session_meta.thread_source")
	if err != nil {
		return rolloutMetadata{}, err
	}
	if payload.Source == nil || payload.Source.Subagent == nil || payload.Source.Subagent.ThreadSpawn == nil {
		return rolloutMetadata{}, unavailable("session_meta.source.subagent.thread_spawn is missing", nil)
	}
	spawn := payload.Source.Subagent.ThreadSpawn
	spawnParentID, err := requiredJSONString(
		spawn.ParentThreadID,
		"session_meta.source.subagent.thread_spawn.parent_thread_id",
	)
	if err != nil {
		return rolloutMetadata{}, err
	}
	spawnAgentRole, err := attestedJSONString(
		spawn.AgentRole,
		"session_meta.source.subagent.thread_spawn.agent_role",
	)
	if err != nil {
		return rolloutMetadata{}, err
	}
	agentRole, err := attestedJSONString(payload.AgentRole, "session_meta.agent_role")
	if err != nil {
		return rolloutMetadata{}, err
	}
	if agentRole != spawnAgentRole {
		return rolloutMetadata{}, mismatch("session_meta agent_role fields disagree")
	}
	multiAgentVersion, err := attestedJSONString(
		payload.MultiAgentVersion,
		"session_meta.multi_agent_version",
	)
	if err != nil {
		return rolloutMetadata{}, err
	}
	if multiAgentVersion != "v1" && multiAgentVersion != "v2" {
		return rolloutMetadata{}, mismatch("session_meta.multi_agent_version is not v1 or v2")
	}
	forkedFromID := ""
	if len(payload.ForkedFromID) != 0 &&
		!bytes.Equal(bytes.TrimSpace(payload.ForkedFromID), []byte("null")) {
		forkedFromID, err = attestedJSONString(
			payload.ForkedFromID,
			"session_meta.forked_from_id",
		)
		if err != nil {
			return rolloutMetadata{}, err
		}
	}
	agentPath := ""
	if multiAgentVersion == "v2" {
		agentPath, err = attestedJSONString(payload.AgentPath, "session_meta.agent_path")
		if err != nil {
			return rolloutMetadata{}, err
		}
		spawnAgentPath, pathErr := attestedJSONString(
			spawn.AgentPath,
			"session_meta.source.subagent.thread_spawn.agent_path",
		)
		if pathErr != nil {
			return rolloutMetadata{}, pathErr
		}
		if spawnAgentPath != agentPath {
			return rolloutMetadata{}, mismatch("child agent_path fields disagree")
		}
	}
	return rolloutMetadata{
		sessionID:           id,
		parentThreadID:      parentID,
		spawnParentThreadID: spawnParentID,
		threadSource:        threadSource,
		agentRole:           agentRole,
		agentPath:           agentPath,
		multiAgentVersion:   multiAgentVersion,
		forkedFromID:        forkedFromID,
		sessionCreatedAt:    createdAt,
	}, nil
}

type turnContextPayload struct {
	Model           json.RawMessage `json:"model"`
	ReasoningEffort json.RawMessage `json:"effort"`
	ApprovalPolicy  json.RawMessage `json:"approval_policy"`
	SandboxPolicy   *struct {
		Type json.RawMessage `json:"type"`
	} `json:"sandbox_policy"`
}

func parseTurnContext(raw json.RawMessage) (rolloutTurnContext, error) {
	var payload turnContextPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return rolloutTurnContext{}, unavailable("malformed turn_context payload", err)
	}
	model, err := attestedJSONString(payload.Model, "turn_context.model")
	if err != nil {
		return rolloutTurnContext{}, err
	}
	reasoningEffort, err := attestedJSONString(
		payload.ReasoningEffort,
		"turn_context.effort",
	)
	if err != nil {
		return rolloutTurnContext{}, err
	}
	if payload.SandboxPolicy == nil {
		return rolloutTurnContext{}, unavailable("turn_context.sandbox_policy is missing", nil)
	}
	sandboxMode, err := attestedJSONString(
		payload.SandboxPolicy.Type,
		"turn_context.sandbox_policy.type",
	)
	if err != nil {
		return rolloutTurnContext{}, err
	}
	approvalPolicy, err := attestedJSONString(
		payload.ApprovalPolicy,
		"turn_context.approval_policy",
	)
	if err != nil {
		return rolloutTurnContext{}, err
	}
	return rolloutTurnContext{
		model:           model,
		reasoningEffort: reasoningEffort,
		sandboxMode:     sandboxMode,
		approvalPolicy:  approvalPolicy,
	}, nil
}

func parseRolloutInput(
	raw json.RawMessage,
) (*rolloutInput, *rolloutInput, bool, error) {
	var payload struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Author    json.RawMessage `json:"author"`
		Recipient json.RawMessage `json:"recipient"`
		Content   []struct {
			Type             string          `json:"type"`
			Text             json.RawMessage `json:"text"`
			EncryptedContent json.RawMessage `json:"encrypted_content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, false, unavailable("malformed response_item payload", err)
	}
	if payload.Type != "message" && payload.Type != "agent_message" {
		return nil, nil, false, nil
	}
	if payload.Type == "message" && payload.Role != "user" {
		return nil, nil, false, nil
	}
	input := rolloutInput{}
	encrypted := false
	for index, item := range payload.Content {
		switch item.Type {
		case "input_text":
			text, err := requiredJSONString(
				item.Text,
				fmt.Sprintf("response_item.content[%d].text", index),
			)
			if err != nil {
				return nil, nil, false, err
			}
			input.texts = append(input.texts, text)
		case "encrypted_content":
			if len(item.EncryptedContent) == 0 ||
				bytes.Equal(bytes.TrimSpace(item.EncryptedContent), []byte("null")) {
				return nil, nil, false, unavailable(
					fmt.Sprintf("response_item.content[%d].encrypted_content is missing", index),
					nil,
				)
			}
			encrypted = true
		default:
			return nil, nil, false, unavailable(
				fmt.Sprintf("response_item.content[%d] has unsupported type %q", index, item.Type),
				nil,
			)
		}
	}
	if payload.Type == "message" {
		return &input, nil, false, nil
	}
	input.author, _ = optionalJSONString(payload.Author)
	input.recipient, _ = optionalJSONString(payload.Recipient)
	return nil, &input, encrypted, nil
}

func parseSandboxOverrideRequest(raw json.RawMessage) (bool, error) {
	var payload struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, unavailable("malformed response_item payload", err)
	}
	switch {
	case payload.Type == "function_call" && payload.Name == "exec_command":
		argumentsText, err := requiredJSONString(
			payload.Arguments,
			"reviewer exec_command arguments",
		)
		if err != nil {
			return false, err
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal([]byte(argumentsText), &arguments); err != nil || arguments == nil {
			return false, unavailable("reviewer exec_command arguments are not a JSON object", err)
		}
		_, requested := arguments["sandbox_permissions"]
		return requested, nil
	case payload.Type == "custom_tool_call" && payload.Name == "exec":
		input, err := requiredJSONString(payload.Input, "reviewer code-mode exec input")
		if err != nil {
			return false, err
		}
		requested, _, err := scanJSExecCommands(input, 0, false)
		return requested, err
	default:
		return false, nil
	}
}

// scanJSExecCommands accepts only direct tools.exec_command calls with a
// literal top-level argument object. Dynamic tool aliases, computed tool
// access, and non-literal argument objects cannot prove that sandbox escape
// fields were absent and therefore fail closed.
func scanJSExecCommands(
	input string,
	start int,
	stopAtClosingBrace bool,
) (bool, int, error) {
	braceDepth := 0
	lastCanEndExpression := false
	lastToken := ""
	controlParenPending := false
	controlParens := make([]bool, 0, 4)
	delimiters := make([]byte, 0, 8)
	for offset := start; offset < len(input); {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		case '/':
			next, comment, err := skipJSComment(input, offset)
			if err != nil {
				return false, 0, err
			}
			if comment {
				offset = next
				continue
			}
			if !lastCanEndExpression {
				offset, err = scanJSRegexLiteral(input, offset)
				if err != nil {
					return false, 0, err
				}
				lastCanEndExpression = true
				lastToken = "value"
				continue
			}
			lastCanEndExpression = false
			lastToken = "/"
			offset++
		case '\\':
			return false, 0, unavailable(
				"reviewer code-mode exec uses an identifier escape",
				nil,
			)
		case '\'', '"':
			value, escaped, next, err := scanJSQuotedString(input, offset)
			if err != nil {
				return false, 0, err
			}
			computed, err := jsStringIsComputedToolName(input, next)
			if err != nil {
				return false, 0, err
			}
			if computed && (escaped || value == "tools" || value == "exec_command") {
				return false, 0, unavailable(
					"reviewer code-mode exec uses computed tool access",
					nil,
				)
			}
			objectKey, err := jsStringIsObjectKey(input, next)
			if err != nil {
				return false, 0, err
			}
			if objectKey && (escaped || isJSDangerousProperty(value)) {
				return false, 0, unavailable(
					"reviewer code-mode exec uses a dangerous object key",
					nil,
				)
			}
			lastCanEndExpression = true
			lastToken = "value"
			offset = next
		case '`':
			requested, next, err := scanJSTemplate(input, offset+1)
			if err != nil {
				return false, 0, err
			}
			if requested {
				return true, next, nil
			}
			lastCanEndExpression = true
			lastToken = "value"
			offset = next
		case '{':
			braceDepth++
			delimiters = append(delimiters, '{')
			lastCanEndExpression = false
			lastToken = "{"
			offset++
		case '}':
			if stopAtClosingBrace && braceDepth == 0 {
				return false, offset + 1, nil
			}
			if braceDepth > 0 {
				braceDepth--
			}
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '{')
			if !matched {
				return false, 0, unavailable(
					"reviewer code-mode exec has mismatched delimiters",
					nil,
				)
			}
			lastCanEndExpression = true
			lastToken = "}"
			offset++
		case '[':
			if lastCanEndExpression {
				next, numeric, err := scanJSStaticNumericIndex(input, offset)
				if err != nil {
					return false, 0, err
				}
				if numeric {
					lastCanEndExpression = true
					lastToken = "]"
					offset = next
					continue
				}
			}
			if lastCanEndExpression || lastToken == "{" ||
				(lastToken == "," && jsDelimiterTop(delimiters) == '{') {
				return false, 0, unavailable(
					"reviewer code-mode exec uses computed member access",
					nil,
				)
			}
			lastCanEndExpression = false
			lastToken = "["
			delimiters = append(delimiters, '[')
			offset++
		case ']':
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '[')
			if !matched {
				return false, 0, unavailable(
					"reviewer code-mode exec has mismatched delimiters",
					nil,
				)
			}
			lastCanEndExpression = true
			lastToken = "]"
			offset++
		case '(':
			delimiters = append(delimiters, '(')
			controlParens = append(controlParens, controlParenPending)
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = "("
			offset++
		case ')':
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '(')
			if !matched {
				return false, 0, unavailable(
					"reviewer code-mode exec has mismatched delimiters",
					nil,
				)
			}
			controlParen := false
			if len(controlParens) > 0 {
				controlParen = controlParens[len(controlParens)-1]
				controlParens = controlParens[:len(controlParens)-1]
			}
			lastCanEndExpression = !controlParen
			controlParenPending = false
			lastToken = ")"
			offset++
		case '?':
			if offset+1 < len(input) && input[offset+1] == '.' {
				return false, 0, unavailable(
					"reviewer code-mode exec uses optional member access",
					nil,
				)
			}
			lastCanEndExpression = false
			lastToken = "?"
			offset++
		case '+', '-':
			if offset+1 < len(input) && input[offset+1] == input[offset] {
				return false, 0, unavailable(
					"reviewer code-mode exec uses an update operator",
					nil,
				)
			}
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = string(input[offset])
			offset++
		case '=', ',', ':', ';', '!', '&', '|', '*', '%', '^', '~', '<', '>':
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = string(input[offset])
			offset++
		default:
			if input[offset] == '#' {
				return false, 0, unavailable(
					"reviewer code-mode exec uses a private identifier",
					nil,
				)
			}
			if input[offset] >= 0x80 {
				return false, 0, unavailable(
					"reviewer code-mode exec uses a non-ASCII identifier",
					nil,
				)
			}
			if isJSIdentifierStart(input[offset]) {
				propertyIdentifier := lastToken == "."
				end := offset + 1
				for end < len(input) && isJSIdentifierContinue(input[end]) {
					end++
				}
				switch input[offset:end] {
				case "tools":
					controlParenPending = false
					requested, methodEnd, err := inspectJSToolsReference(input, end)
					if err != nil {
						return false, 0, err
					}
					if requested {
						return true, methodEnd, nil
					}
					lastCanEndExpression = true
					lastToken = "identifier"
					offset = methodEnd
					continue
				case "exec_command":
					return false, 0, unavailable(
						"reviewer code-mode exec calls exec_command through an alias",
						nil,
					)
				case "eval", "Function", "AsyncFunction", "GeneratorFunction",
					"global", "globalThis", "window", "self", "this", "process",
					"require", "module", "import", "Object", "Reflect", "Proxy", "constructor",
					"prototype", "__proto__":
					return false, 0, unavailable(
						"reviewer code-mode exec uses dynamic JavaScript access",
						nil,
					)
				}
				if propertyIdentifier {
					controlParenPending = false
					lastCanEndExpression = true
				} else {
					switch input[offset:end] {
					case "if", "while", "for", "with":
						controlParenPending = true
						lastCanEndExpression = false
					case "const", "let", "var", "return", "throw", "case", "new",
						"delete", "void", "typeof", "instanceof", "in", "of",
						"yield", "await", "else", "do":
						controlParenPending = false
						lastCanEndExpression = false
					default:
						controlParenPending = false
						lastCanEndExpression = true
					}
				}
				lastToken = "identifier"
				offset = end
				continue
			}
			switch {
			case input[offset] >= '0' && input[offset] <= '9':
				controlParenPending = false
				lastCanEndExpression = true
				lastToken = "value"
			case input[offset] == '.':
				controlParenPending = false
				lastToken = "."
			default:
				controlParenPending = false
				lastCanEndExpression = false
				lastToken = string(input[offset])
			}
			offset++
		}
	}
	if stopAtClosingBrace {
		return false, 0, unavailable(
			"reviewer code-mode exec has an unterminated template interpolation",
			nil,
		)
	}
	return false, len(input), nil
}

func inspectJSToolsReference(input string, start int) (bool, int, error) {
	offset, err := skipJSTrivia(input, start)
	if err != nil {
		return false, 0, err
	}
	if offset >= len(input) || input[offset] != '.' {
		return false, 0, unavailable("reviewer code-mode exec aliases tools", nil)
	}
	offset, err = skipJSTrivia(input, offset+1)
	if err != nil {
		return false, 0, err
	}
	if offset >= len(input) || !isJSIdentifierStart(input[offset]) {
		return false, 0, unavailable("reviewer code-mode exec has a dynamic tool name", nil)
	}
	methodStart := offset
	offset++
	for offset < len(input) && isJSIdentifierContinue(input[offset]) {
		offset++
	}
	if input[methodStart:offset] != "exec_command" {
		return false, 0, unavailable(
			"reviewer code-mode exec calls an unsupported tool",
			nil,
		)
	}
	openParen, err := skipJSTrivia(input, offset)
	if err != nil {
		return false, 0, err
	}
	if openParen >= len(input) || input[openParen] != '(' {
		return false, 0, unavailable("reviewer code-mode exec aliases exec_command", nil)
	}
	requested, err := inspectJSExecCommandArguments(input, openParen)
	return requested, offset, err
}

func inspectJSExecCommandArguments(input string, openParen int) (bool, error) {
	offset, err := skipJSTrivia(input, openParen+1)
	if err != nil {
		return false, err
	}
	if offset >= len(input) || input[offset] != '{' {
		return false, unavailable(
			"reviewer code-mode exec_command arguments are not a literal object",
			nil,
		)
	}
	requested, objectEnd, err := inspectJSExecCommandObject(input, offset)
	if err != nil || requested {
		return requested, err
	}
	offset, err = skipJSTrivia(input, objectEnd)
	if err != nil {
		return false, err
	}
	if offset >= len(input) || input[offset] != ')' {
		return false, unavailable(
			"reviewer code-mode exec_command has noncanonical arguments",
			nil,
		)
	}
	return false, nil
}

func inspectJSExecCommandObject(input string, openBrace int) (bool, int, error) {
	offset := openBrace + 1
	seen := make(map[string]struct{})
	for {
		var key string
		var escaped bool
		var err error
		offset, err = skipJSTrivia(input, offset)
		if err != nil {
			return false, 0, err
		}
		if offset >= len(input) {
			return false, 0, unavailable(
				"reviewer code-mode exec_command object is unterminated",
				nil,
			)
		}
		if input[offset] == '}' {
			return false, offset + 1, nil
		}
		switch {
		case input[offset] == '[' || strings.HasPrefix(input[offset:], "..."):
			return false, 0, unavailable(
				"reviewer code-mode exec_command uses a dynamic object key",
				nil,
			)
		case input[offset] == '\'' || input[offset] == '"':
			key, escaped, offset, err = scanJSQuotedString(input, offset)
			if err != nil {
				return false, 0, err
			}
			if escaped {
				return false, 0, unavailable(
					"reviewer code-mode exec_command uses an escaped object key",
					nil,
				)
			}
		case isJSIdentifierStart(input[offset]):
			start := offset
			offset++
			for offset < len(input) && isJSIdentifierContinue(input[offset]) {
				offset++
			}
			key = input[start:offset]
		default:
			return false, 0, unavailable(
				"reviewer code-mode exec_command object key is not static",
				nil,
			)
		}
		if _, exists := seen[key]; exists {
			return false, 0, unavailable(
				"reviewer code-mode exec_command object key is duplicated",
				nil,
			)
		}
		seen[key] = struct{}{}
		offset, err = skipJSTrivia(input, offset)
		if err != nil {
			return false, 0, err
		}
		if offset >= len(input) || input[offset] != ':' {
			return false, 0, unavailable(
				"reviewer code-mode exec_command object key has no literal value",
				nil,
			)
		}
		if key == "sandbox_permissions" {
			return true, offset + 1, nil
		}
		switch key {
		case "cmd", "workdir", "yield_time_ms", "max_output_tokens", "shell", "login", "tty":
		default:
			return false, 0, unavailable(
				"reviewer code-mode exec_command uses an unsupported argument",
				nil,
			)
		}
		delimiter, next, err := scanJSObjectValue(input, offset+1)
		if err != nil {
			return false, 0, err
		}
		switch delimiter {
		case ',':
			offset = next
		case '}':
			return false, next, nil
		default:
			return false, 0, unavailable(
				"reviewer code-mode exec_command object value is unterminated",
				nil,
			)
		}
	}
}

func scanJSObjectValue(input string, start int) (byte, int, error) {
	offset, err := skipJSTrivia(input, start)
	if err != nil {
		return 0, 0, err
	}
	valueStart := offset
	closers := make([]byte, 0, 4)
	for offset < len(input) {
		switch input[offset] {
		case '\'', '"':
			_, _, offset, err = scanJSQuotedString(input, offset)
			if err != nil {
				return 0, 0, err
			}
			continue
		case '`':
			_, offset, err = scanJSTemplate(input, offset+1)
			if err != nil {
				return 0, 0, err
			}
			continue
		case '/':
			next, comment, commentErr := skipJSComment(input, offset)
			if commentErr != nil {
				return 0, 0, commentErr
			}
			if comment {
				offset = next
				continue
			}
			if jsRegexCanStart(input, offset) {
				offset, err = scanJSRegexLiteral(input, offset)
				if err != nil {
					return 0, 0, err
				}
				continue
			}
		case '(', '[', '{':
			closer := map[byte]byte{'(': ')', '[': ']', '{': '}'}[input[offset]]
			closers = append(closers, closer)
			offset++
			continue
		case ')', ']':
			if len(closers) == 0 || closers[len(closers)-1] != input[offset] {
				return 0, 0, unavailable(
					"reviewer code-mode exec_command object value is malformed",
					nil,
				)
			}
			closers = closers[:len(closers)-1]
			offset++
			continue
		case '}':
			if len(closers) == 0 {
				if offset == valueStart {
					return 0, 0, unavailable(
						"reviewer code-mode exec_command object value is missing",
						nil,
					)
				}
				return '}', offset + 1, nil
			}
			if closers[len(closers)-1] != '}' {
				return 0, 0, unavailable(
					"reviewer code-mode exec_command object value is malformed",
					nil,
				)
			}
			closers = closers[:len(closers)-1]
			offset++
			continue
		case ',':
			if len(closers) == 0 {
				if offset == valueStart {
					return 0, 0, unavailable(
						"reviewer code-mode exec_command object value is missing",
						nil,
					)
				}
				return ',', offset + 1, nil
			}
		}
		offset++
	}
	return 0, 0, unavailable(
		"reviewer code-mode exec_command object value is unterminated",
		nil,
	)
}

func scanJSQuotedString(input string, start int) (string, bool, int, error) {
	quote := input[start]
	contentStart := start + 1
	escaped := false
	for offset := contentStart; offset < len(input); offset++ {
		switch input[offset] {
		case quote:
			return input[contentStart:offset], escaped, offset + 1, nil
		case '\\':
			escaped = true
			offset++
			if offset >= len(input) {
				return "", false, 0, unavailable(
					"reviewer code-mode exec has an unterminated string escape",
					nil,
				)
			}
		case '\r', '\n':
			return "", false, 0, unavailable(
				"reviewer code-mode exec has an unterminated quoted string",
				nil,
			)
		}
	}
	return "", false, 0, unavailable(
		"reviewer code-mode exec has an unterminated quoted string",
		nil,
	)
}

func scanJSTemplate(input string, start int) (bool, int, error) {
	for offset := start; offset < len(input); offset++ {
		switch input[offset] {
		case '\\':
			offset++
			if offset >= len(input) {
				return false, 0, unavailable(
					"reviewer code-mode exec has an unterminated template escape",
					nil,
				)
			}
		case '`':
			return false, offset + 1, nil
		case '$':
			if offset+1 >= len(input) || input[offset+1] != '{' {
				continue
			}
			requested, next, err := scanJSExecCommands(input, offset+2, true)
			if err != nil {
				return false, 0, err
			}
			if requested {
				return true, next, nil
			}
			offset = next - 1
		}
	}
	return false, 0, unavailable(
		"reviewer code-mode exec has an unterminated template literal",
		nil,
	)
}

func jsStringIsComputedToolName(input string, start int) (bool, error) {
	offset, err := skipJSTrivia(input, start)
	if err != nil {
		return false, err
	}
	return offset < len(input) && input[offset] == ']', nil
}

func scanJSStaticNumericIndex(input string, openBracket int) (int, bool, error) {
	offset, err := skipJSTrivia(input, openBracket+1)
	if err != nil {
		return 0, false, err
	}
	start := offset
	for offset < len(input) && input[offset] >= '0' && input[offset] <= '9' {
		offset++
	}
	if offset == start {
		return 0, false, nil
	}
	offset, err = skipJSTrivia(input, offset)
	if err != nil {
		return 0, false, err
	}
	if offset >= len(input) || input[offset] != ']' {
		return 0, false, nil
	}
	return offset + 1, true, nil
}

func jsDelimiterTop(delimiters []byte) byte {
	if len(delimiters) == 0 {
		return 0
	}
	return delimiters[len(delimiters)-1]
}

func popJSDelimiter(delimiters []byte, expected byte) ([]byte, bool) {
	if jsDelimiterTop(delimiters) != expected {
		return delimiters, false
	}
	return delimiters[:len(delimiters)-1], true
}

func jsStringIsObjectKey(input string, start int) (bool, error) {
	offset, err := skipJSTrivia(input, start)
	if err != nil {
		return false, err
	}
	return offset < len(input) && input[offset] == ':', nil
}

func isJSDangerousProperty(value string) bool {
	switch value {
	case "constructor", "prototype", "__proto__", "tools", "exec_command":
		return true
	default:
		return false
	}
}

func skipJSTrivia(input string, start int) (int, error) {
	for offset := start; offset < len(input); {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		case '/':
			next, comment, err := skipJSComment(input, offset)
			if err != nil {
				return 0, err
			}
			if !comment {
				return offset, nil
			}
			offset = next
		default:
			return offset, nil
		}
	}
	return len(input), nil
}

func skipJSComment(input string, start int) (int, bool, error) {
	if start+1 >= len(input) || input[start] != '/' {
		return start, false, nil
	}
	switch input[start+1] {
	case '/':
		if newline := strings.IndexByte(input[start+2:], '\n'); newline >= 0 {
			return start + 2 + newline + 1, true, nil
		}
		return len(input), true, nil
	case '*':
		if closing := strings.Index(input[start+2:], "*/"); closing >= 0 {
			return start + 2 + closing + 2, true, nil
		}
		return 0, true, unavailable(
			"reviewer code-mode exec has an unterminated block comment",
			nil,
		)
	default:
		return start, false, nil
	}
}

func jsRegexCanStart(input string, slash int) bool {
	offset := slash - 1
	for offset >= 0 {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset--
		default:
			const beforeExpression = "=([{,:;!&|?+-*%^~<>"
			if strings.ContainsRune(beforeExpression, rune(input[offset])) {
				return true
			}
			if !isJSIdentifierContinue(input[offset]) {
				return false
			}
			end := offset + 1
			for offset >= 0 && isJSIdentifierContinue(input[offset]) {
				offset--
			}
			switch input[offset+1 : end] {
			case "return", "throw", "case", "delete", "void", "typeof",
				"instanceof", "in", "of", "yield", "await":
				return true
			default:
				return false
			}
		}
	}
	return true
}

func scanJSRegexLiteral(input string, start int) (int, error) {
	inClass := false
	for offset := start + 1; offset < len(input); offset++ {
		switch input[offset] {
		case '\\':
			offset++
			if offset >= len(input) {
				return 0, unavailable(
					"reviewer code-mode exec has an unterminated regex escape",
					nil,
				)
			}
		case '[':
			if !inClass {
				inClass = true
			}
		case ']':
			if inClass {
				inClass = false
			}
		case '/':
			if inClass {
				continue
			}
			offset++
			for offset < len(input) && isJSIdentifierContinue(input[offset]) {
				offset++
			}
			return offset, nil
		case '\r', '\n':
			return 0, unavailable(
				"reviewer code-mode exec has an unterminated regex literal",
				nil,
			)
		}
	}
	return 0, unavailable(
		"reviewer code-mode exec has an unterminated regex literal",
		nil,
	)
}

func optionalJSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", false
	}
	return value, true
}

type parentSpawnCall struct {
	callID               string
	namespace            string
	createdAt            time.Time
	arguments            map[string]json.RawMessage
	controllerTurnID     string
	controllerContext    ControllerContextAttestation
	hasControllerContext bool
	recordNumber         int
}

type parentToolInvocation struct {
	callID             string
	createdAt          time.Time
	turnID             string
	recordNumber       int
	outputAt           time.Time
	outputTurnID       string
	outputRecordNumber int
	hasOutput          bool
}

type parentToolOutput struct {
	createdAt    time.Time
	turnID       string
	recordNumber int
	hasTimestamp bool
}

type parentRolloutEvidence struct {
	activities  map[string]parentSpawnActivity
	invocations []parentToolInvocation
}

type parentSpawnActivity struct {
	sessionID string
	agentPath string
}

func validateParentSpawn(
	path string,
	child rolloutMetadata,
	parentThreadID string,
	agentRole string,
	expectedBundlePath string,
	countAfter time.Time,
	freshAfter time.Time,
	expectedControllerTurnID string,
	expectedControllerContextSHA256 string,
	spawnAuthorizedAt time.Time,
	reviewerSpawns *int,
	attestedController *ControllerContextAttestation,
) error {
	calls, outputs, evidence, readErr := readParentSpawnRecords(path, parentThreadID)
	if readErr != nil {
		return readErr
	}
	activities := evidence.activities
	invocations := evidence.invocations
	wantNamespace := map[string]string{
		"v1": "multi_agent_v1",
		"v2": "collaboration",
	}[child.multiAgentVersion]
	matchingSpawnCalls := 0
	var matched []parentSpawnCall
	var v2Candidates []parentSpawnCall
	for _, call := range calls {
		spawnRole, roleOK := optionalJSONString(call.arguments["agent_type"])
		message, messageOK := optionalJSONString(call.arguments["message"])
		nativeNamespace := call.namespace == "multi_agent_v1" || call.namespace == "collaboration"
		if nativeNamespace && call.createdAt.After(countAfter) && roleOK &&
			(spawnRole == "post-work-reviewer" || spawnRole == "post-work-verifier") {
			(*reviewerSpawns)++
		}
		if call.namespace == wantNamespace && call.createdAt.After(countAfter) &&
			((roleOK && spawnRole == agentRole) ||
				(messageOK && strings.Contains(message, expectedBundlePath))) {
			matchingSpawnCalls++
		}
		output, ok := outputs[call.callID]
		if !ok {
			continue
		}
		var result struct {
			AgentID  json.RawMessage `json:"agent_id"`
			TaskName json.RawMessage `json:"task_name"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			continue
		}
		switch child.multiAgentVersion {
		case "v1":
			agentID, ok := optionalJSONString(result.AgentID)
			if ok && agentID == child.sessionID {
				matched = append(matched, call)
			}
		case "v2":
			taskName, ok := optionalJSONString(result.TaskName)
			if ok && taskName == child.agentPath {
				v2Candidates = append(v2Candidates, call)
			}
		}
	}
	if child.multiAgentVersion == "v2" {
		for _, candidate := range v2Candidates {
			activity, ok := activities[candidate.callID]
			if !ok {
				continue
			}
			if activity.sessionID == child.sessionID && activity.agentPath == child.agentPath {
				matched = append(matched, candidate)
			}
		}
		if len(activities) == 0 {
			// Older V2 rollouts may not persist sub_agent_activity. Preserve the
			// single-output fallback, but never guess between repeated task names.
			matched = append(matched, v2Candidates...)
		}
		if len(activities) != 0 && len(matched) == 0 {
			return mismatch("parent V2 spawn activity does not match the child session")
		}
	}
	if len(matched) == 0 {
		return unavailable("parent rollout has no spawn output corresponding to the child", nil)
	}
	if len(matched) != 1 {
		return unavailable(
			fmt.Sprintf("parent rollout has %d spawn outputs corresponding to the child", len(matched)),
			nil,
		)
	}
	call := matched[0]
	if call.namespace != wantNamespace {
		return mismatch("parent spawn namespace does not match the child multi-agent version")
	}
	if err := validateSpawnArgumentFields(call.arguments, child.multiAgentVersion); err != nil {
		return err
	}
	if !call.createdAt.After(freshAfter) {
		return mismatch("parent spawn was not created after the review bundle")
	}
	if !call.createdAt.After(spawnAuthorizedAt) {
		return mismatch("parent spawn was not created after spawn authorization")
	}
	if !child.sessionCreatedAt.After(call.createdAt) {
		return mismatch("reviewer session was not created after the parent spawn")
	}
	if call.controllerTurnID == "" {
		return unavailable("parent spawn controller turn ID is missing", nil)
	}
	if !isCanonicalUUID(call.controllerTurnID) {
		return mismatch("parent spawn controller turn ID is not a canonical UUID")
	}
	if call.controllerTurnID != expectedControllerTurnID {
		return mismatch("parent spawn controller turn ID does not match authorization")
	}
	if !call.hasControllerContext {
		return unavailable("parent spawn has no enclosing controller turn_context", nil)
	}
	if call.controllerContext.TurnID != call.controllerTurnID {
		return mismatch("parent spawn turn ID does not match its controller turn_context")
	}
	if call.controllerContext.ContextSHA256 != expectedControllerContextSHA256 {
		return mismatch("parent spawn controller context digest does not match authorization")
	}
	if call.controllerContext.SandboxMode != controllerReadOnlySandbox {
		return mismatch("parent spawn controller sandbox is not read-only")
	}
	if !spawnAuthorizedAt.After(call.controllerContext.Timestamp) {
		return mismatch("spawn authorization was not created after the controller turn_context")
	}
	if !call.createdAt.After(call.controllerContext.Timestamp) {
		return mismatch("parent spawn was not created after its controller turn_context")
	}
	if err := validateSpawnAuthorizationOrdering(
		invocations,
		call,
		expectedControllerTurnID,
		spawnAuthorizedAt,
	); err != nil {
		return err
	}
	actualRole, err := spawnJSONString(call.arguments, "agent_type")
	if err != nil {
		return err
	}
	if actualRole != agentRole {
		return mismatch("parent spawn agent_type does not match the child role")
	}
	message, err := spawnJSONString(call.arguments, "message")
	if err != nil {
		return err
	}
	if child.multiAgentVersion == "v1" && message != expectedBundlePath {
		return mismatch("parent V1 spawn message is not the expected bundle path")
	}
	if child.multiAgentVersion == "v2" && message != expectedBundlePath &&
		!childHasPlaintextPathInput(child, expectedBundlePath) {
		return mismatch("parent V2 spawn message and child task input do not attest the bundle path")
	}

	switch child.multiAgentVersion {
	case "v1":
		forkContext, err := spawnJSONBool(call.arguments, "fork_context")
		if err != nil {
			return err
		}
		if forkContext {
			return mismatch("parent V1 spawn fork_context is not false")
		}
		if _, exists := call.arguments["fork_turns"]; exists {
			return mismatch("parent V1 spawn unexpectedly contains fork_turns")
		}
	case "v2":
		forkTurns, err := spawnJSONString(call.arguments, "fork_turns")
		if err != nil {
			return err
		}
		if forkTurns != "none" {
			return mismatch("parent V2 spawn fork_turns is not none")
		}
		if _, exists := call.arguments["fork_context"]; exists {
			return mismatch("parent V2 spawn unexpectedly contains fork_context")
		}
		taskName, err := spawnJSONString(call.arguments, "task_name")
		if err != nil {
			return err
		}
		parentPath, ok := parentAgentPath(child.agentPath)
		if !ok || parentPath+"/"+taskName != child.agentPath {
			return mismatch("parent V2 spawn task_name does not match child agent_path")
		}
	default:
		return mismatch("session_meta.multi_agent_version is not v1 or v2")
	}
	if agentRole == "post-work-reviewer" && matchingSpawnCalls != 1 {
		return mismatch(fmt.Sprintf(
			"parent rollout has %d fresh native spawn calls for the review bundle, want 1",
			matchingSpawnCalls,
		))
	}
	*attestedController = call.controllerContext
	return nil
}

func validateSpawnAuthorizationOrdering(
	invocations []parentToolInvocation,
	spawn parentSpawnCall,
	expectedControllerTurnID string,
	spawnAuthorizedAt time.Time,
) error {
	// Codex rollout timestamps are currently emitted at millisecond precision,
	// while the driver records authorization at nanosecond precision. Compare
	// the enclosing call/output interval at the rollout's effective precision
	// so an output recorded later in the same millisecond is not rejected.
	authorizationMillisecond := spawnAuthorizedAt.Truncate(time.Millisecond)
	candidates := make([]parentToolInvocation, 0, 1)
	for _, invocation := range invocations {
		if !invocation.hasOutput || invocation.createdAt.After(authorizationMillisecond) ||
			invocation.outputAt.Before(authorizationMillisecond) {
			continue
		}
		candidates = append(candidates, invocation)
	}
	if len(candidates) == 0 {
		return unavailable(
			"parent rollout has no tool invocation containing spawn authorization",
			nil,
		)
	}
	if len(candidates) != 1 {
		return unavailable(fmt.Sprintf(
			"parent rollout has %d tool invocations containing spawn authorization, want 1",
			len(candidates),
		), nil)
	}
	authorization := candidates[0]
	if authorization.outputRecordNumber <= authorization.recordNumber {
		return mismatch("spawn authorization tool output does not follow its invocation")
	}
	if authorization.turnID == "" || authorization.outputTurnID == "" {
		return unavailable("spawn authorization tool turn ID is missing", nil)
	}
	if !isCanonicalUUID(authorization.turnID) ||
		!isCanonicalUUID(authorization.outputTurnID) {
		return mismatch("spawn authorization tool turn ID is not a canonical UUID")
	}
	if authorization.turnID != expectedControllerTurnID ||
		authorization.outputTurnID != expectedControllerTurnID {
		return mismatch("spawn authorization tool turn ID does not match the controller turn")
	}

	for _, invocation := range invocations {
		if invocation.recordNumber <= authorization.outputRecordNumber {
			continue
		}
		if invocation.callID != spawn.callID {
			return mismatch("another tool invocation appears between spawn authorization and spawn")
		}
		if invocation.turnID != expectedControllerTurnID {
			return mismatch("spawn invocation turn ID does not match the authorization turn")
		}
		return nil
	}
	return unavailable("parent rollout has no tool invocation after spawn authorization", nil)
}

func validateSpawnArgumentFields(arguments map[string]json.RawMessage, version string) error {
	allowed := map[string]struct{}{
		"agent_type": {},
		"message":    {},
	}
	switch version {
	case "v1":
		allowed["fork_context"] = struct{}{}
	case "v2":
		allowed["fork_turns"] = struct{}{}
		allowed["task_name"] = struct{}{}
	default:
		return mismatch("session_meta.multi_agent_version is not v1 or v2")
	}
	for field := range arguments {
		if _, ok := allowed[field]; !ok {
			return mismatch("parent spawn contains unsupported argument " + field)
		}
	}
	return nil
}

func childHasPlaintextPathInput(child rolloutMetadata, expectedBundlePath string) bool {
	for _, input := range child.agentInputs {
		if len(input.texts) == 1 && input.texts[0] == expectedBundlePath {
			return true
		}
	}
	if len(child.userInputs) == 0 {
		return false
	}
	last := child.userInputs[len(child.userInputs)-1]
	return len(last.texts) == 1 && last.texts[0] == expectedBundlePath
}

func readParentSpawnRecords(
	path string,
	parentThreadID string,
) (
	[]parentSpawnCall,
	map[string]string,
	*parentRolloutEvidence,
	error,
) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, unavailable("read parent rollout", err)
	}
	defer func() {
		// Parsing has consumed the file before return; a close failure cannot
		// change the attested records.
		_ = file.Close()
	}()

	decoder := json.NewDecoder(file)
	callsByID := make(map[string]parentSpawnCall)
	codeModeCalls := make(map[string]struct{})
	outputs := make(map[string]string)
	activities := make(map[string]parentSpawnActivity)
	toolCallsByID := make(map[string]parentToolInvocation)
	toolOutputsByID := make(map[string]parentToolOutput)
	parentMetaCount := 0
	var currentControllerContext ControllerContextAttestation
	hasControllerContext := false
	for recordNumber := 1; ; recordNumber++ {
		var record rolloutRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, nil, unavailable(
				fmt.Sprintf("malformed parent rollout record %d", recordNumber),
				err,
			)
		}
		if record.Type == "session_meta" {
			parentMetaCount++
			var payload struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, nil, nil, unavailable("malformed parent session_meta payload", err)
			}
			id, err := requiredJSONString(payload.ID, "parent session_meta.id")
			if err != nil {
				return nil, nil, nil, err
			}
			if id != parentThreadID {
				return nil, nil, nil, mismatch("parent session_meta.id does not match the prepared parent")
			}
			continue
		}
		if record.Type == "turn_context" {
			context, err := parseControllerTurnContextRecord(record, false)
			if err != nil {
				return nil, nil, nil, err
			}
			currentControllerContext = context
			hasControllerContext = true
			continue
		}
		if record.Type == "event_msg" {
			activity, started, err := parseParentSpawnActivity(record.Payload)
			if err != nil {
				return nil, nil, nil, err
			}
			if started {
				if _, exists := activities[activity.callID]; exists {
					return nil, nil, nil, unavailable(
						"parent rollout has duplicate started activity for spawn call_id",
						nil,
					)
				}
				activities[activity.callID] = parentSpawnActivity{
					sessionID: activity.sessionID,
					agentPath: activity.agentPath,
				}
			}
			continue
		}
		if record.Type != "response_item" {
			continue
		}
		var payload struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Namespace string          `json:"namespace"`
			CallID    json.RawMessage `json:"call_id"`
			Arguments json.RawMessage `json:"arguments"`
			Input     json.RawMessage `json:"input"`
			Output    json.RawMessage `json:"output"`
			Metadata  *struct {
				TurnID json.RawMessage `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return nil, nil, nil, unavailable("malformed parent response_item payload", err)
		}
		if payload.Type == "function_call" || payload.Type == "custom_tool_call" {
			timestamp, err := requiredJSONString(record.Timestamp, "parent tool invocation timestamp")
			if err != nil {
				return nil, nil, nil, err
			}
			createdAt, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				return nil, nil, nil, unavailable(
					"parent tool invocation timestamp is not RFC3339",
					err,
				)
			}
			callID, err := requiredJSONString(payload.CallID, "parent tool invocation call_id")
			if err != nil {
				return nil, nil, nil, err
			}
			if _, exists := toolCallsByID[callID]; exists {
				return nil, nil, nil, unavailable("parent rollout has duplicate tool call_id", nil)
			}
			turnID := ""
			if payload.Metadata != nil {
				turnID, _ = optionalJSONString(payload.Metadata.TurnID)
			}
			toolCallsByID[callID] = parentToolInvocation{
				callID:       callID,
				createdAt:    createdAt,
				turnID:       turnID,
				recordNumber: recordNumber,
			}
		}
		if payload.Type == "function_call_output" || payload.Type == "custom_tool_call_output" {
			callID, ok := optionalJSONString(payload.CallID)
			if ok {
				if _, exists := toolOutputsByID[callID]; exists {
					return nil, nil, nil, unavailable(
						"parent rollout has duplicate tool output call_id",
						nil,
					)
				}
				timestamp, err := requiredJSONString(record.Timestamp, "parent tool output timestamp")
				if err != nil {
					return nil, nil, nil, err
				}
				createdAt, err := time.Parse(time.RFC3339Nano, timestamp)
				if err != nil {
					return nil, nil, nil, unavailable(
						"parent tool output timestamp is not RFC3339",
						err,
					)
				}
				turnID := ""
				if payload.Metadata != nil {
					turnID, _ = optionalJSONString(payload.Metadata.TurnID)
				}
				toolOutputsByID[callID] = parentToolOutput{
					createdAt:    createdAt,
					turnID:       turnID,
					recordNumber: recordNumber,
					hasTimestamp: true,
				}
			}
		}
		switch payload.Type {
		case "function_call":
			if payload.Name != "spawn_agent" {
				continue
			}
			timestamp, err := requiredJSONString(record.Timestamp, "parent spawn timestamp")
			if err != nil {
				return nil, nil, nil, err
			}
			createdAt, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				return nil, nil, nil, unavailable("parent spawn timestamp is not RFC3339", err)
			}
			callID, err := requiredJSONString(payload.CallID, "parent spawn call_id")
			if err != nil {
				return nil, nil, nil, err
			}
			argumentsText, err := requiredJSONString(payload.Arguments, "parent spawn arguments")
			if err != nil {
				return nil, nil, nil, err
			}
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal([]byte(argumentsText), &arguments); err != nil || arguments == nil {
				return nil, nil, nil, unavailable("parent spawn arguments are not a JSON object", err)
			}
			if _, exists := callsByID[callID]; exists {
				return nil, nil, nil, unavailable("parent rollout has duplicate spawn call_id", nil)
			}
			controllerTurnID := ""
			if payload.Metadata != nil {
				controllerTurnID, _ = optionalJSONString(payload.Metadata.TurnID)
			}
			callsByID[callID] = parentSpawnCall{
				callID:               callID,
				namespace:            payload.Namespace,
				createdAt:            createdAt,
				arguments:            arguments,
				controllerTurnID:     controllerTurnID,
				controllerContext:    currentControllerContext,
				hasControllerContext: hasControllerContext,
				recordNumber:         recordNumber,
			}
		case "custom_tool_call":
			if payload.Name != "exec" {
				continue
			}
			input, ok := optionalJSONString(payload.Input)
			if !ok {
				continue
			}
			arguments, isSpawn, err := parseV1CodeModeSpawnWrapper(input)
			if err != nil {
				return nil, nil, nil, unavailable(
					"parent V1 code-mode spawn wrapper is not canonical",
					err,
				)
			}
			if !isSpawn {
				continue
			}
			timestamp, err := requiredJSONString(record.Timestamp, "parent spawn timestamp")
			if err != nil {
				return nil, nil, nil, err
			}
			createdAt, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				return nil, nil, nil, unavailable("parent spawn timestamp is not RFC3339", err)
			}
			callID, err := requiredJSONString(payload.CallID, "parent spawn call_id")
			if err != nil {
				return nil, nil, nil, err
			}
			if _, exists := callsByID[callID]; exists {
				return nil, nil, nil, unavailable("parent rollout has duplicate spawn call_id", nil)
			}
			if _, exists := outputs[callID]; exists {
				return nil, nil, nil, unavailable(
					"parent V1 code-mode spawn output precedes its call",
					nil,
				)
			}
			controllerTurnID := ""
			if payload.Metadata != nil {
				controllerTurnID, _ = optionalJSONString(payload.Metadata.TurnID)
			}
			callsByID[callID] = parentSpawnCall{
				callID:               callID,
				namespace:            "multi_agent_v1",
				createdAt:            createdAt,
				arguments:            arguments,
				controllerTurnID:     controllerTurnID,
				controllerContext:    currentControllerContext,
				hasControllerContext: hasControllerContext,
				recordNumber:         recordNumber,
			}
			codeModeCalls[callID] = struct{}{}
		case "function_call_output":
			callID, ok := optionalJSONString(payload.CallID)
			if !ok {
				continue
			}
			if _, isCodeMode := codeModeCalls[callID]; isCodeMode {
				return nil, nil, nil, unavailable(
					"parent V1 code-mode spawn uses a legacy function_call_output",
					nil,
				)
			}
			output, ok := optionalJSONString(payload.Output)
			if !ok {
				continue
			}
			if _, exists := outputs[callID]; exists {
				return nil, nil, nil, unavailable("parent rollout has duplicate function_call_output", nil)
			}
			outputs[callID] = output
		case "custom_tool_call_output":
			callID, ok := optionalJSONString(payload.CallID)
			if !ok {
				continue
			}
			if _, isCodeMode := codeModeCalls[callID]; !isCodeMode {
				continue
			}
			output, err := parseV1CodeModeSpawnOutput(payload.Output)
			if err != nil {
				return nil, nil, nil, unavailable(
					"parent V1 code-mode spawn output is not canonical",
					err,
				)
			}
			if _, exists := outputs[callID]; exists {
				return nil, nil, nil, unavailable("parent rollout has duplicate spawn output", nil)
			}
			outputs[callID] = output
		}
	}
	if parentMetaCount != 1 {
		return nil, nil, nil, unavailable(
			fmt.Sprintf("parent rollout contains %d session_meta records, want 1", parentMetaCount),
			nil,
		)
	}
	calls := make([]parentSpawnCall, 0, len(callsByID))
	for _, call := range callsByID {
		calls = append(calls, call)
	}
	invocations := make([]parentToolInvocation, 0, len(toolCallsByID))
	for callID, invocation := range toolCallsByID {
		if output, ok := toolOutputsByID[callID]; ok && output.hasTimestamp {
			invocation.outputAt = output.createdAt
			invocation.outputTurnID = output.turnID
			invocation.outputRecordNumber = output.recordNumber
			invocation.hasOutput = true
		}
		invocations = append(invocations, invocation)
	}
	sort.Slice(invocations, func(i, j int) bool {
		return invocations[i].recordNumber < invocations[j].recordNumber
	})
	return calls, outputs, &parentRolloutEvidence{
		activities:  activities,
		invocations: invocations,
	}, nil
}

const v1CodeModeSpawnTool = "tools.multi_agent_v1__spawn_agent"

// parseV1CodeModeSpawnWrapper accepts only the complete code-mode wrapper
// emitted for a native V1 spawn. Keeping this grammar narrower than JavaScript
// prevents dynamic expressions or adjacent statements from becoming spawn
// attestation evidence.
func parseV1CodeModeSpawnWrapper(input string) (map[string]json.RawMessage, bool, error) {
	isSpawn, _, err := scanV1SpawnToolAccess(input, 0, false)
	if err != nil {
		return nil, true, err
	}
	if !isSpawn {
		return nil, false, nil
	}

	parser := v1SpawnWrapperParser{input: input}
	parser.skipWhitespace()
	if !parser.consume("const") || !parser.consumeWhitespace() {
		return nil, true, errors.New("wrapper must start with a const declaration")
	}
	variable, ok := parser.identifier()
	if !ok {
		return nil, true, errors.New("wrapper result variable is missing")
	}
	parser.skipWhitespace()
	if !parser.consume("=") {
		return nil, true, errors.New("wrapper const declaration has no assignment")
	}
	parser.skipWhitespace()
	if !parser.consume("await") || !parser.consumeWhitespace() {
		return nil, true, errors.New("wrapper spawn call is not directly awaited")
	}
	if !parser.consume(v1CodeModeSpawnTool) {
		return nil, true, errors.New("wrapper does not call the native V1 spawn tool")
	}
	parser.skipWhitespace()
	if !parser.consume("(") {
		return nil, true, errors.New("wrapper spawn call is missing its argument list")
	}
	parser.skipWhitespace()
	arguments, err := parser.spawnArguments()
	if err != nil {
		return nil, true, err
	}
	parser.skipWhitespace()
	if !parser.consume(")") {
		return nil, true, errors.New("wrapper spawn call is not closed")
	}
	parser.skipWhitespace()
	if !parser.consume(";") {
		return nil, true, errors.New("wrapper spawn call has no terminating semicolon")
	}
	parser.skipWhitespace()
	if !parser.consume("text") {
		return nil, true, errors.New("wrapper does not project the spawn result with text")
	}
	parser.skipWhitespace()
	if !parser.consume("(") {
		return nil, true, errors.New("wrapper text call is missing its argument list")
	}
	parser.skipWhitespace()
	jsonStringified := parser.consume("JSON.stringify")
	if jsonStringified {
		parser.skipWhitespace()
		if !parser.consume("(") {
			return nil, true, errors.New("wrapper JSON.stringify call is missing its argument list")
		}
		parser.skipWhitespace()
	}
	projectedVariable, ok := parser.identifier()
	if !ok || projectedVariable != variable {
		return nil, true, errors.New("wrapper projects a different result variable")
	}
	parser.skipWhitespace()
	if jsonStringified && !parser.consume(")") {
		return nil, true, errors.New("wrapper JSON.stringify call is not closed")
	}
	parser.skipWhitespace()
	if !parser.consume(")") {
		return nil, true, errors.New("wrapper text call is not closed")
	}
	parser.skipWhitespace()
	if !parser.consume(";") {
		return nil, true, errors.New("wrapper text call has no terminating semicolon")
	}
	parser.skipWhitespace()
	if !parser.atEnd() {
		return nil, true, errors.New("wrapper contains an extra statement")
	}
	return arguments, true, nil
}

func scanV1SpawnToolAccess(
	input string,
	start int,
	stopAtClosingBrace bool,
) (bool, int, error) {
	braceDepth := 0
	lastCanEndExpression := false
	lastToken := ""
	controlParenPending := false
	controlParens := make([]bool, 0, 4)
	delimiters := make([]byte, 0, 8)
	for offset := start; offset < len(input); {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		case '/':
			next, comment, err := skipJSComment(input, offset)
			if err != nil {
				return false, 0, err
			}
			if comment {
				offset = next
				continue
			}
			if !lastCanEndExpression {
				offset, err = scanJSRegexLiteral(input, offset)
				if err != nil {
					return false, 0, err
				}
				lastCanEndExpression = true
				lastToken = "value"
				continue
			}
			lastCanEndExpression = false
			lastToken = "/"
			offset++
		case '\\':
			return false, 0, errors.New("wrapper uses an identifier escape")
		case '+', '-':
			if offset+1 < len(input) && input[offset+1] == input[offset] {
				return false, 0, errors.New("wrapper uses an update operator")
			}
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = string(input[offset])
			offset++
		case '\'', '"':
			value, escaped, next, err := scanJSQuotedString(input, offset)
			if err != nil {
				return false, 0, err
			}
			objectKey, err := jsStringIsObjectKey(input, next)
			if err != nil {
				return false, 0, err
			}
			if objectKey && (escaped || isJSDangerousProperty(value)) {
				return false, 0, errors.New("wrapper uses a dangerous object key")
			}
			lastCanEndExpression = true
			lastToken = "value"
			offset = next
		case '`':
			found, next, err := scanV1SpawnTemplate(input, offset+1)
			if err != nil {
				return false, 0, err
			}
			if found {
				return true, next, nil
			}
			lastCanEndExpression = true
			lastToken = "value"
			offset = next
		case '{':
			braceDepth++
			delimiters = append(delimiters, '{')
			lastCanEndExpression = false
			lastToken = "{"
			offset++
		case '}':
			if stopAtClosingBrace && braceDepth == 0 {
				return false, offset + 1, nil
			}
			if braceDepth > 0 {
				braceDepth--
			}
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '{')
			if !matched {
				return false, 0, errors.New("wrapper has mismatched delimiters")
			}
			lastCanEndExpression = true
			lastToken = "}"
			offset++
		case '[':
			if lastCanEndExpression {
				next, numeric, err := scanJSStaticNumericIndex(input, offset)
				if err != nil {
					return false, 0, err
				}
				if numeric {
					lastCanEndExpression = true
					lastToken = "]"
					offset = next
					continue
				}
			}
			if lastCanEndExpression || lastToken == "{" ||
				(lastToken == "," && jsDelimiterTop(delimiters) == '{') {
				return false, 0, errors.New("wrapper uses computed member access")
			}
			lastCanEndExpression = false
			lastToken = "["
			delimiters = append(delimiters, '[')
			offset++
		case ']':
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '[')
			if !matched {
				return false, 0, errors.New("wrapper has mismatched delimiters")
			}
			lastCanEndExpression = true
			lastToken = "]"
			offset++
		case '(':
			delimiters = append(delimiters, '(')
			controlParens = append(controlParens, controlParenPending)
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = "("
			offset++
		case ')':
			var matched bool
			delimiters, matched = popJSDelimiter(delimiters, '(')
			if !matched {
				return false, 0, errors.New("wrapper has mismatched delimiters")
			}
			controlParen := false
			if len(controlParens) > 0 {
				controlParen = controlParens[len(controlParens)-1]
				controlParens = controlParens[:len(controlParens)-1]
			}
			lastCanEndExpression = !controlParen
			controlParenPending = false
			lastToken = ")"
			offset++
		case '?':
			if offset+1 < len(input) && input[offset+1] == '.' {
				return false, 0, errors.New("wrapper uses optional member access")
			}
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = "?"
			offset++
		case '=', ',', ':', ';', '!', '&', '|', '*', '%', '^', '~', '<', '>':
			controlParenPending = false
			lastCanEndExpression = false
			lastToken = string(input[offset])
			offset++
		default:
			if input[offset] == '#' {
				return false, 0, errors.New("wrapper uses a private identifier")
			}
			if input[offset] >= 0x80 {
				return false, 0, errors.New("wrapper uses a non-ASCII identifier")
			}
			if !isJSIdentifierStart(input[offset]) {
				switch {
				case input[offset] >= '0' && input[offset] <= '9':
					controlParenPending = false
					lastCanEndExpression = true
					lastToken = "value"
				case input[offset] == '.':
					controlParenPending = false
					lastToken = "."
				default:
					controlParenPending = false
					lastCanEndExpression = false
					lastToken = string(input[offset])
				}
				offset++
				continue
			}
			end := offset + 1
			for end < len(input) && isJSIdentifierContinue(input[end]) {
				end++
			}
			identifier := input[offset:end]
			propertyIdentifier := lastToken == "."
			switch identifier {
			case "tools":
				controlParenPending = false
				found, next, err := inspectV1ToolsReference(input, end)
				if err != nil {
					return false, 0, err
				}
				if found {
					return true, next, nil
				}
				lastCanEndExpression = true
				lastToken = "identifier"
				offset = next
				continue
			case "eval", "Function", "AsyncFunction", "GeneratorFunction",
				"global", "globalThis", "window", "self", "this", "process",
				"require", "module", "import", "Object", "Reflect", "Proxy",
				"constructor", "prototype", "__proto__":
				return false, 0, errors.New("wrapper uses dynamic JavaScript access")
			}
			if strings.HasPrefix(identifier, "multi_agent_v1__spawn") {
				return false, 0, errors.New("wrapper aliases the native V1 spawn tool")
			}
			if propertyIdentifier {
				controlParenPending = false
				lastCanEndExpression = true
			} else {
				switch identifier {
				case "if", "while", "for", "with":
					controlParenPending = true
					lastCanEndExpression = false
				case "const", "let", "var", "return", "throw", "case", "new",
					"delete", "void", "typeof", "instanceof", "in", "of",
					"yield", "await", "else", "do":
					controlParenPending = false
					lastCanEndExpression = false
				default:
					controlParenPending = false
					lastCanEndExpression = true
				}
			}
			lastToken = "identifier"
			offset = end
			continue
		}
	}
	if stopAtClosingBrace {
		return false, 0, errors.New("wrapper has an unterminated template interpolation")
	}
	return false, len(input), nil
}

func inspectV1ToolsReference(input string, start int) (bool, int, error) {
	offset, err := skipJSTrivia(input, start)
	if err != nil {
		return false, 0, err
	}
	if offset >= len(input) || input[offset] != '.' {
		return false, 0, errors.New("wrapper uses dynamic or aliased tools access")
	}
	offset, err = skipJSTrivia(input, offset+1)
	if err != nil {
		return false, 0, err
	}
	if offset >= len(input) || !isJSIdentifierStart(input[offset]) {
		return false, 0, errors.New("wrapper uses a dynamic tool name")
	}
	methodStart := offset
	offset++
	for offset < len(input) && isJSIdentifierContinue(input[offset]) {
		offset++
	}
	method := input[methodStart:offset]
	if method == "multi_agent_v1__spawn_agent" {
		return true, offset, nil
	}
	if strings.HasPrefix(method, "multi_agent_v1__spawn") {
		return false, 0, errors.New("wrapper uses a noncanonical native V1 spawn tool name")
	}
	return false, offset, nil
}

func scanV1SpawnTemplate(input string, start int) (bool, int, error) {
	for offset := start; offset < len(input); offset++ {
		switch input[offset] {
		case '\\':
			offset++
			if offset >= len(input) {
				return false, 0, errors.New("wrapper has an unterminated template escape")
			}
		case '`':
			return false, offset + 1, nil
		case '$':
			if offset+1 >= len(input) || input[offset+1] != '{' {
				continue
			}
			found, next, err := scanV1SpawnToolAccess(input, offset+2, true)
			if err != nil {
				return false, 0, err
			}
			if found {
				return true, next, nil
			}
			offset = next - 1
		}
	}
	return false, 0, errors.New("wrapper has an unterminated template literal")
}

type v1SpawnWrapperParser struct {
	input  string
	offset int
}

func (p *v1SpawnWrapperParser) atEnd() bool {
	return p.offset == len(p.input)
}

func (p *v1SpawnWrapperParser) consume(value string) bool {
	if !strings.HasPrefix(p.input[p.offset:], value) {
		return false
	}
	p.offset += len(value)
	return true
}

func (p *v1SpawnWrapperParser) consumeWhitespace() bool {
	start := p.offset
	p.skipWhitespace()
	return p.offset > start
}

func (p *v1SpawnWrapperParser) skipWhitespace() {
	for p.offset < len(p.input) {
		switch p.input[p.offset] {
		case ' ', '\t', '\r', '\n':
			p.offset++
		default:
			return
		}
	}
}

func (p *v1SpawnWrapperParser) identifier() (string, bool) {
	if p.offset >= len(p.input) || !isJSIdentifierStart(p.input[p.offset]) {
		return "", false
	}
	start := p.offset
	p.offset++
	for p.offset < len(p.input) && isJSIdentifierContinue(p.input[p.offset]) {
		p.offset++
	}
	return p.input[start:p.offset], true
}

func isJSIdentifierStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z'
}

func isJSIdentifierContinue(value byte) bool {
	return isJSIdentifierStart(value) || value >= '0' && value <= '9'
}

func (p *v1SpawnWrapperParser) spawnArguments() (map[string]json.RawMessage, error) {
	if !p.consume("{") {
		return nil, errors.New("wrapper spawn arguments are not an object literal")
	}
	p.skipWhitespace()
	arguments := make(map[string]json.RawMessage, 3)
	for {
		field, ok := p.identifier()
		if !ok {
			return nil, errors.New("wrapper spawn argument name is missing")
		}
		if _, exists := arguments[field]; exists {
			return nil, fmt.Errorf("wrapper spawn argument %s is duplicated", field)
		}
		p.skipWhitespace()
		if !p.consume(":") {
			return nil, fmt.Errorf("wrapper spawn argument %s has no literal value", field)
		}
		p.skipWhitespace()
		switch field {
		case "agent_type", "message":
			literal, err := p.jsonStringLiteral()
			if err != nil {
				return nil, fmt.Errorf("wrapper spawn argument %s: %w", field, err)
			}
			arguments[field] = literal
		case "fork_context":
			switch {
			case p.consume("false"):
				arguments[field] = json.RawMessage("false")
			case p.consume("true"):
				arguments[field] = json.RawMessage("true")
			default:
				return nil, errors.New("wrapper spawn argument fork_context is not a boolean literal")
			}
		default:
			return nil, fmt.Errorf("wrapper spawn contains unsupported argument %s", field)
		}
		p.skipWhitespace()
		switch {
		case p.consume(","):
			p.skipWhitespace()
			if strings.HasPrefix(p.input[p.offset:], "}") {
				return nil, errors.New("wrapper spawn argument object has a trailing comma")
			}
		case p.consume("}"):
			for _, required := range []string{"agent_type", "message", "fork_context"} {
				if _, exists := arguments[required]; !exists {
					return nil, fmt.Errorf("wrapper spawn argument %s is missing", required)
				}
			}
			return arguments, nil
		default:
			return nil, errors.New("wrapper spawn arguments are not comma-separated")
		}
	}
}

func (p *v1SpawnWrapperParser) jsonStringLiteral() (json.RawMessage, error) {
	if p.offset >= len(p.input) || p.input[p.offset] != '"' {
		return nil, errors.New("value is not a JSON string literal")
	}
	start := p.offset
	p.offset++
	for p.offset < len(p.input) {
		switch p.input[p.offset] {
		case '"':
			p.offset++
			literal := json.RawMessage(p.input[start:p.offset])
			var value string
			if err := json.Unmarshal(literal, &value); err != nil {
				return nil, errors.New("value is not a valid JSON string literal")
			}
			return literal, nil
		case '\\':
			p.offset += 2
		default:
			p.offset++
		}
	}
	return nil, errors.New("JSON string literal is not closed")
}

func parseV1CodeModeSpawnOutput(raw json.RawMessage) (string, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) != 2 {
		return "", errors.New("output must contain exactly two blocks")
	}
	status, err := parseCanonicalInputTextBlock(blocks[0])
	if err != nil || !isCanonicalExecStatus(status) {
		return "", errors.New("output status block is not canonical")
	}
	resultText, err := parseCanonicalInputTextBlock(blocks[1])
	if err != nil {
		return "", errors.New("output result block is not canonical input_text")
	}
	result, err := decodeUniqueJSONObject([]byte(resultText))
	if err != nil {
		return "", errors.New("output result block is not a JSON object")
	}
	for field := range result {
		if field != "agent_id" && field != "nickname" {
			return "", fmt.Errorf("output result contains unsupported field %s", field)
		}
	}
	agentID, err := requiredJSONString(result["agent_id"], "parent spawn output agent_id")
	if err != nil {
		return "", errors.New("output result agent_id is missing")
	}
	if nickname, exists := result["nickname"]; exists {
		if _, nicknameErr := requiredJSONString(nickname, "parent spawn output nickname"); nicknameErr != nil {
			return "", errors.New("output result nickname is not a non-empty string")
		}
	}
	normalized, err := json.Marshal(struct {
		AgentID string `json:"agent_id"`
	}{AgentID: agentID})
	if err != nil {
		return "", fmt.Errorf("normalize output result: %w", err)
	}
	return string(normalized), nil
}

func parseCanonicalInputTextBlock(raw json.RawMessage) (string, error) {
	block, err := decodeUniqueJSONObject(raw)
	if err != nil || len(block) != 2 {
		return "", errors.New("block is not a two-field object")
	}
	typeName, err := requiredJSONString(block["type"], "output block type")
	if err != nil || typeName != "input_text" {
		return "", errors.New("block type is not input_text")
	}
	return requiredJSONString(block["text"], "output block text")
}

func decodeUniqueJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("value is not a JSON object")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("JSON object field name is malformed")
		}
		field, ok := fieldToken.(string)
		if !ok {
			return nil, errors.New("JSON object field name is not a string")
		}
		if _, exists := result[field]; exists {
			return nil, fmt.Errorf("JSON object field %s is duplicated", field)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("JSON object field value is malformed")
		}
		result[field] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("JSON object is not closed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON object has trailing data")
	}
	return result, nil
}

func isCanonicalExecStatus(value string) bool {
	const (
		prefix = "Script completed\nWall time "
		suffix = " seconds\nOutput:\n"
	)
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return false
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if duration == "" {
		return false
	}
	dotSeen := false
	digitSeen := false
	for index := range len(duration) {
		switch {
		case duration[index] >= '0' && duration[index] <= '9':
			digitSeen = true
		case duration[index] == '.' && !dotSeen && index > 0 && index < len(duration)-1:
			dotSeen = true
		default:
			return false
		}
	}
	return digitSeen
}

type parsedParentSpawnActivity struct {
	callID    string
	sessionID string
	agentPath string
}

func parseParentSpawnActivity(raw json.RawMessage) (parsedParentSpawnActivity, bool, error) {
	var payload struct {
		Type          string          `json:"type"`
		Kind          string          `json:"kind"`
		EventID       json.RawMessage `json:"event_id"`
		AgentThreadID json.RawMessage `json:"agent_thread_id"`
		AgentPath     json.RawMessage `json:"agent_path"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return parsedParentSpawnActivity{}, false, unavailable(
			"malformed parent event_msg payload",
			err,
		)
	}
	if payload.Type != "sub_agent_activity" || payload.Kind != "started" {
		return parsedParentSpawnActivity{}, false, nil
	}
	callID, err := requiredJSONString(payload.EventID, "parent started activity event_id")
	if err != nil {
		return parsedParentSpawnActivity{}, false, err
	}
	sessionID, err := requiredJSONString(
		payload.AgentThreadID,
		"parent started activity agent_thread_id",
	)
	if err != nil {
		return parsedParentSpawnActivity{}, false, err
	}
	if !isCanonicalUUID(sessionID) {
		return parsedParentSpawnActivity{}, false, mismatch(
			"parent started activity agent_thread_id is not a canonical UUID",
		)
	}
	agentPath, err := requiredJSONString(payload.AgentPath, "parent started activity agent_path")
	if err != nil {
		return parsedParentSpawnActivity{}, false, err
	}
	return parsedParentSpawnActivity{
		callID:    callID,
		sessionID: sessionID,
		agentPath: agentPath,
	}, true, nil
}

func spawnJSONString(arguments map[string]json.RawMessage, field string) (string, error) {
	raw, ok := arguments[field]
	if !ok {
		return "", mismatch("parent spawn " + field + " is missing")
	}
	value, ok := optionalJSONString(raw)
	if !ok {
		return "", mismatch("parent spawn " + field + " is not a non-empty string")
	}
	return value, nil
}

func spawnJSONBool(arguments map[string]json.RawMessage, field string) (bool, error) {
	raw, ok := arguments[field]
	if !ok {
		return false, mismatch("parent spawn " + field + " is missing")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, mismatch("parent spawn " + field + " is not a boolean")
	}
	return value, nil
}

func parseTaskComplete(raw json.RawMessage) (bool, string, bool, error) {
	var payload struct {
		Type             string          `json:"type"`
		LastAgentMessage json.RawMessage `json:"last_agent_message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, "", false, unavailable("malformed event_msg payload", err)
	}
	if payload.Type != "task_complete" {
		return false, "", false, nil
	}
	message, err := requiredJSONString(payload.LastAgentMessage, "task_complete.last_agent_message")
	if err != nil {
		return true, "", false, err
	}
	return true, message, true, nil
}

func requiredJSONString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", unavailable(field+" is missing", nil)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", unavailable(field+" is not a string", err)
	}
	if value == "" {
		return "", unavailable(field+" is empty", nil)
	}
	return value, nil
}

func attestedJSONString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 {
		return "", unavailable(field+" is missing", nil)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", mismatch(field + " is null")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", unavailable(field+" is not a string", err)
	}
	if value == "" {
		return "", mismatch(field + " is empty")
	}
	return value, nil
}

func validateRollout(
	metadata rolloutMetadata,
	resultData []byte,
	sessionID string,
	parentThreadID string,
	preparedAt time.Time,
	config agentConfig,
	expectedBundlePath string,
) error {
	if metadata.sessionID != sessionID {
		return mismatch("session_meta.id does not match reviewer_session_id")
	}
	if !isCanonicalUUID(metadata.sessionID) {
		return mismatch("session_meta.id is not a canonical UUID")
	}
	if metadata.parentThreadID != parentThreadID {
		return mismatch("session_meta.parent_thread_id does not match the prepared parent")
	}
	if metadata.spawnParentThreadID != parentThreadID {
		return mismatch("thread_spawn.parent_thread_id does not match the prepared parent")
	}
	if metadata.threadSource != "subagent" {
		return mismatch("session_meta.thread_source is not subagent")
	}
	if metadata.agentRole != config.name {
		return mismatch("child agent_role does not match the agent config")
	}
	if metadata.forkedFromID != "" {
		return mismatch("session_meta.forked_from_id proves inherited history")
	}
	if err := validateTaskInput(metadata, expectedBundlePath); err != nil {
		return err
	}
	if !metadata.sessionCreatedAt.After(preparedAt) {
		return mismatch("reviewer session was not created after prepare")
	}
	if len(metadata.turnContexts) == 0 {
		return unavailable("rollout contains no turn_context records", nil)
	}
	for _, context := range metadata.turnContexts {
		if context.model != config.model {
			return mismatch("child model does not match the agent config")
		}
		if context.reasoningEffort != config.reasoningEffort {
			return mismatch("child reasoning effort does not match the agent config")
		}
		if context.sandboxMode != config.sandboxMode {
			return mismatch("child sandbox policy does not match the agent config")
		}
		if context.approvalPolicy != config.approvalPolicy {
			return mismatch("child approval policy does not match the agent config")
		}
	}
	if metadata.taskCompleteCount != 1 {
		return mismatch(fmt.Sprintf(
			"rollout contains %d task_complete records, want 1",
			metadata.taskCompleteCount,
		))
	}
	if !metadata.lastAgentMessageSeen {
		return unavailable("task_complete.last_agent_message is missing", nil)
	}
	if !transcriptMatches(resultData, metadata.lastAgentMessage) {
		return mismatch("reviewer JSON does not match task_complete.last_agent_message")
	}
	return nil
}

func validateTaskInput(metadata rolloutMetadata, expectedBundlePath string) error {
	isExactPath := func(input rolloutInput) bool {
		return len(input.texts) == 1 && input.texts[0] == expectedBundlePath
	}
	switch metadata.multiAgentVersion {
	case "v1":
		if len(metadata.agentInputs) != 0 || metadata.encryptedAgentInputs != 0 {
			return mismatch("V1 child contains unexpected inter-agent task input")
		}
		matchingPathInputs := 0
		lastMatches := false
		for index, input := range metadata.userInputs {
			if isExactPath(input) {
				matchingPathInputs++
				lastMatches = index == len(metadata.userInputs)-1
			}
		}
		if matchingPathInputs != 1 || !lastMatches {
			return mismatch("V1 child has no unique final path-only task input")
		}
	case "v2":
		if metadata.encryptedAgentInputs != 0 {
			return mismatch("V2 child task input is encrypted and cannot attest the bundle path")
		}
		if len(metadata.agentInputs) == 1 {
			input := metadata.agentInputs[0]
			if !isExactPath(input) {
				return mismatch("V2 child task input is not the expected bundle path")
			}
			if input.recipient != metadata.agentPath {
				return mismatch("V2 child task recipient does not match child agent_path")
			}
			parentPath, ok := parentAgentPath(metadata.agentPath)
			if !ok || input.author != parentPath {
				return mismatch("V2 child task author does not match parent agent_path")
			}
			return nil
		}
		if len(metadata.agentInputs) != 0 {
			return mismatch("V2 child contains multiple task inputs")
		}
		matchingPathInputs := 0
		lastMatches := false
		for index, input := range metadata.userInputs {
			if isExactPath(input) {
				matchingPathInputs++
				lastMatches = index == len(metadata.userInputs)-1
			}
		}
		if matchingPathInputs != 1 || !lastMatches {
			return mismatch("V2 child has no unique final path-only task input")
		}
	default:
		return mismatch("session_meta.multi_agent_version is not v1 or v2")
	}
	return nil
}

func parentAgentPath(childPath string) (string, bool) {
	separator := strings.LastIndexByte(childPath, '/')
	if separator <= 0 || separator == len(childPath)-1 {
		return "", false
	}
	return childPath[:separator], true
}

func transcriptMatches(resultData []byte, message string) bool {
	if bytes.Equal(resultData, []byte(message)) {
		return true
	}
	return len(resultData) > 0 && resultData[len(resultData)-1] == '\n' &&
		bytes.Equal(resultData[:len(resultData)-1], []byte(message))
}

func clearAttestationCache(outputDir string) error {
	info, err := os.Stat(outputDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("reviewer JSON cache path is not a directory")
	}
	for _, name := range attestationCacheFiles {
		err := os.Remove(filepath.Join(outputDir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

func writeAttestationFailure(outputDir string, attestationErr *AttestationError) error {
	if err := os.Remove(filepath.Join(outputDir, attestationValidFile)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := atomicfs.WriteFile(
		filepath.Join(outputDir, "attestation_error"),
		[]byte(attestationErr.Error()),
		0o600,
	); err != nil {
		return err
	}
	return atomicfs.WriteFile(
		filepath.Join(outputDir, "attestation_error_kind"),
		[]byte(attestationErr.Kind),
		0o600,
	)
}

func writeAttestationSuccess(outputDir string, value attestation) error {
	files := []projectedFile{
		{name: "attestation_version", data: []byte(AttestationVersion)},
		{name: "attested_session_id", data: []byte(value.sessionID)},
		{name: "attested_parent_thread_id", data: []byte(value.parentThreadID)},
		{name: "attested_agent_role", data: []byte(value.agentRole)},
		{name: "attested_model", data: []byte(value.model)},
		{name: "attested_sandbox_mode", data: []byte(value.sandboxMode)},
		{name: "attested_approval_policy", data: []byte(value.approvalPolicy)},
		{name: "attested_history_mode", data: []byte(value.historyMode)},
		{name: "attested_reviewer_spawn_calls", data: []byte(strconv.Itoa(value.reviewerSpawns))},
		{name: "attested_controller_turn_id", data: []byte(value.controllerTurnID)},
		{name: "attested_controller_context_sha256", data: []byte(value.controllerContextSHA256)},
		{name: "attested_controller_sandbox_mode", data: []byte(value.controllerSandboxMode)},
		{name: "attested_spawn_authorized_at", data: []byte(value.spawnAuthorizedAt)},
	}
	for _, file := range files {
		if err := atomicfs.WriteFile(filepath.Join(outputDir, file.name), file.data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", file.name, err)
		}
	}
	if err := os.Remove(filepath.Join(outputDir, "attestation_error")); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(outputDir, "attestation_error_kind")); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfs.WriteFile(filepath.Join(outputDir, attestationValidFile), nil, 0o600)
}

func unavailable(detail string, cause error) *AttestationError {
	return &AttestationError{Kind: AttestationUnavailable, detail: detail, cause: cause}
}

func mismatch(detail string) *AttestationError {
	return &AttestationError{Kind: AttestationMismatch, detail: detail}
}

func reused(detail string) *AttestationError {
	return &AttestationError{Kind: AttestationReused, detail: detail}
}

func asAttestationError(err error) *AttestationError {
	var attestationErr *AttestationError
	if errors.As(err, &attestationErr) {
		return attestationErr
	}
	return unavailable("unexpected attestation failure", err)
}
