package herdrrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	ownedBehaviorProfileID     = "herdr-wave2-behavior-v1"
	ownedBehaviorSource        = "official-release-asset"
	ownedBehaviorVersion       = "0.7.5"
	ownedBehaviorGOOS          = "darwin"
	ownedBehaviorGOARCH        = "arm64"
	ownedBehaviorBinarySHA256  = "37350546b0012555943b92eaf962665de4e264395baeb44227b8015e8ff5b0d6"
	ownedBehaviorSchemaSHA256  = "1ef4eb9ec655cb0c89726895f437d8654bdde13a22e591fda06a9015d03d88c7"
	ownedDetectionFixtureID    = "herdr-0.7.5-darwin-arm64-bundled-agent-manifests-v1"
	ownedManifestSetDigest     = "71e6a97c86625960aa0ca86a018998bfa9679387c7586b5564ffddd83c5c0784"
	ownedNoRefreshPolicyID     = "herdr-manifest-check-disabled-v1"
	manifestDigestDomain       = "fanout.herdr-agent-manifest-set.v1\n"
	agentManifestResponseID    = "cli:server:agent-manifests"
	agentManifestResponseType  = "agent_manifest_status"
	agentManifestBundledSource = "bundled"
)

type manifestFixtureEntry struct {
	agent   string
	version string
}

type behaviorProfile struct {
	id                string
	source            string
	version           string
	goos              string
	goarch            string
	binarySHA256      string
	schemaSHA256      string
	detectionFixture  string
	manifestSetDigest string
	noRefreshPolicy   string
	manifests         []manifestFixtureEntry
}

type behaviorAdmission struct {
	profile behaviorProfile
}

var ownedManifestFixture = []manifestFixtureEntry{
	{agent: "agy", version: "2026.06.24.1"},
	{agent: "amp", version: "2026.07.09.1"},
	{agent: "claude", version: "2026.07.13.1"},
	{agent: "cline", version: "2026.06.10.1"},
	{agent: "codex", version: "2026.07.18.1"},
	{agent: "copilot", version: "2026.07.07.1"},
	{agent: "cursor", version: "2026.06.10.1"},
	{agent: "devin", version: "2026.06.15.1"},
	{agent: "droid", version: "2026.06.10.1"},
	{agent: "gemini", version: "2026.06.10.1"},
	{agent: "grok", version: "2026.07.16.2"},
	{agent: "hermes", version: "2026.06.10.1"},
	{agent: "kilo", version: "2026.06.10.1"},
	{agent: "kimi", version: "2026.06.10.1"},
	{agent: "kiro", version: "2026.06.10.1"},
	{agent: "maki", version: "2026.07.09.2"},
	{agent: "opencode", version: "2026.06.10.1"},
	{agent: "pi", version: "2026.06.10.1"},
	{agent: "qodercli", version: "2026.06.10.1"},
}

func productionBehaviorProfile() behaviorProfile {
	return behaviorProfile{
		id: ownedBehaviorProfileID, source: ownedBehaviorSource, version: ownedBehaviorVersion,
		goos: ownedBehaviorGOOS, goarch: ownedBehaviorGOARCH, binarySHA256: ownedBehaviorBinarySHA256,
		schemaSHA256:     ownedBehaviorSchemaSHA256,
		detectionFixture: ownedDetectionFixtureID, manifestSetDigest: ownedManifestSetDigest,
		noRefreshPolicy: ownedNoRefreshPolicyID, manifests: slices.Clone(ownedManifestFixture),
	}
}

func (b *Backend) admitOwnedBehavior(admitted binaryAdmission) (behaviorAdmission, error) {
	if b == nil {
		return behaviorAdmission{}, fmt.Errorf("herdr owned behavior admission requires a backend")
	}
	profile := b.behavior
	if err := validateBehaviorProfile(profile, admitted); err != nil {
		return behaviorAdmission{}, err
	}
	return behaviorAdmission{profile: profile}, nil
}

func validateBehaviorProfile(profile behaviorProfile, admitted binaryAdmission) error {
	if profile.id == "" || profile.source == "" || profile.detectionFixture == "" || profile.noRefreshPolicy == "" ||
		!validHexToken(profile.binarySHA256) || !validHexToken(profile.schemaSHA256) || !validHexToken(profile.manifestSetDigest) {
		return fmt.Errorf("herdr owned behavior profile is incomplete")
	}
	if profile.goos != runtime.GOOS || profile.goarch != runtime.GOARCH {
		return fmt.Errorf("unsupported herdr owned behavior platform %s/%s (profile requires %s/%s)", runtime.GOOS, runtime.GOARCH, profile.goos, profile.goarch)
	}
	if admitted.version != profile.version || admitted.sha256 != profile.binarySHA256 || admitted.schemaSHA256 != profile.schemaSHA256 {
		return fmt.Errorf("herdr binary is outside owned behavior profile %s", profile.id)
	}
	if got := manifestFixtureDigest(profile.manifests, profile.binarySHA256); got != profile.manifestSetDigest {
		return fmt.Errorf("herdr owned behavior profile manifest digest is %s, want %s", got, profile.manifestSetDigest)
	}
	if ownedConfigContents != "[update]\nmanifest_check = false\n" {
		return fmt.Errorf("herdr owned no-refresh policy bytes changed")
	}
	return nil
}

func behaviorAdmissionID(
	profile behaviorProfile,
	admitted binaryAdmission,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	ownerNonce string,
) string {
	parts := []string{
		"fanout.herdr-behavior-admission.v1", profile.id, profile.source, profile.version,
		profile.goos, profile.goarch, profile.binarySHA256, profile.detectionFixture,
		profile.schemaSHA256, profile.manifestSetDigest, profile.noRefreshPolicy, admitted.path, admitted.sha256,
		admitted.schemaSHA256, admitted.version, fmt.Sprintf("%d", admitted.protocol), commonDir,
		fmt.Sprintf("%d", commonIdentity.device), fmt.Sprintf("%d", commonIdentity.inode),
		layout.runtimeDir, filepath.Base(layout.runtimeDir), layout.socketPath, layout.clientSocketPath,
		layout.configPath, ownerNonce,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func manifestFixtureDigest(entries []manifestFixtureEntry, binarySHA256 string) string {
	// The reviewed manifests are bundled inside the exact official executable.
	// Binding its digest fixes their raw bytes; the normalized active set below
	// proves that the connected server selected that bundled fixture without an
	// override or cached remote replacement.
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(left, right manifestFixtureEntry) int { return strings.Compare(left.agent, right.agent) })
	var payload strings.Builder
	payload.WriteString(manifestDigestDomain)
	fmt.Fprintf(&payload, "binary_sha256\t%s\n", binarySHA256)
	for _, entry := range ordered {
		fmt.Fprintf(&payload, "%s\t%s\t%s\t%s\tfalse\n", entry.agent, entry.version, agentManifestBundledSource, agentManifestBundledSource)
	}
	sum := sha256.Sum256([]byte(payload.String()))
	return hex.EncodeToString(sum[:])
}

type agentManifestEnvelope struct {
	ID     string               `json:"id"`
	Result *agentManifestResult `json:"result"`
}

type agentManifestResult struct {
	Type          string              `json:"type"`
	Manifests     []agentManifestInfo `json:"manifests"`
	LastCheckUnix json.RawMessage     `json:"last_check_unix,omitempty"`
	LastResult    json.RawMessage     `json:"last_result,omitempty"`
}

type agentManifestInfo struct {
	Agent                        string          `json:"agent"`
	Source                       string          `json:"source"`
	SourceKind                   string          `json:"source_kind"`
	LocalOverrideShadowingRemote *bool           `json:"local_override_shadowing_remote"`
	ActiveVersion                json.RawMessage `json:"active_version,omitempty"`
	CachedRemoteVersion          json.RawMessage `json:"cached_remote_version,omitempty"`
	RemoteLastCheckedUnix        json.RawMessage `json:"remote_last_checked_unix,omitempty"`
	RemoteUpdateError            json.RawMessage `json:"remote_update_error,omitempty"`
	RemoteUpdateResult           json.RawMessage `json:"remote_update_result,omitempty"`
	Warning                      json.RawMessage `json:"warning,omitempty"`
}

func (b *Backend) validateActiveManifestProfile(ctx context.Context, probed probeResult, profile behaviorProfile) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "server", "agent-manifests", "--json")
	if err != nil {
		return fmt.Errorf("herdr server agent-manifests --json: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	var envelope agentManifestEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("parse herdr active agent manifests: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("parse herdr active agent manifests: unexpected trailing JSON value")
		}
		return fmt.Errorf("parse herdr active agent manifests: %w", err)
	}
	if envelope.ID != agentManifestResponseID || envelope.Result == nil || envelope.Result.Type != agentManifestResponseType ||
		len(envelope.Result.LastCheckUnix) != 0 || len(envelope.Result.LastResult) != 0 {
		return fmt.Errorf("herdr active agent manifests do not satisfy the no-refresh response profile")
	}
	observed := make([]manifestFixtureEntry, 0, len(envelope.Result.Manifests))
	seen := make(map[string]struct{}, len(envelope.Result.Manifests))
	for _, manifest := range envelope.Result.Manifests {
		if manifest.Agent == "" || manifest.Source != agentManifestBundledSource || manifest.SourceKind != agentManifestBundledSource ||
			manifest.LocalOverrideShadowingRemote == nil || *manifest.LocalOverrideShadowingRemote ||
			len(manifest.CachedRemoteVersion) != 0 || len(manifest.RemoteLastCheckedUnix) != 0 || len(manifest.RemoteUpdateError) != 0 ||
			len(manifest.RemoteUpdateResult) != 0 || len(manifest.Warning) != 0 {
			return fmt.Errorf("herdr active manifest %q violates the bundled no-refresh fixture", manifest.Agent)
		}
		if _, duplicate := seen[manifest.Agent]; duplicate {
			return fmt.Errorf("herdr active manifest fixture contains duplicate agent %q", manifest.Agent)
		}
		seen[manifest.Agent] = struct{}{}
		var version string
		if len(manifest.ActiveVersion) == 0 || json.Unmarshal(manifest.ActiveVersion, &version) != nil || version == "" {
			return fmt.Errorf("herdr active manifest %q has no exact active version", manifest.Agent)
		}
		observed = append(observed, manifestFixtureEntry{agent: manifest.Agent, version: version})
	}
	if len(observed) != len(profile.manifests) || manifestFixtureDigest(observed, profile.binarySHA256) != profile.manifestSetDigest {
		return fmt.Errorf("herdr active manifest set does not match fixture %s", profile.detectionFixture)
	}
	return nil
}
