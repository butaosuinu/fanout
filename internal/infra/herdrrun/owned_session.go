package herdrrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type OwnedOptions struct {
	GitCommonDir string
	RuntimeBase  string
}

type OwnedSession struct {
	Session          string
	SocketPath       string
	ClientSocketPath string
	GitCommonDir     string
	RuntimeDir       string
	LauncherPath     string
	EmitterPath      string
	ControlPath      string

	backend          *Backend
	processInspector paneProcessInspector
}

func (s *OwnedSession) Backend() *Backend {
	if s == nil {
		return nil
	}
	return s.backend
}

// AttachForms builds both attach forms from one verified owner marker: the
// process image a terminal execs to enter the owned session in place, and the
// equivalent shell command for printing. One admission serves both, so the two
// forms can never diverge and the probe cost is paid once. The forms differ in
// one documented way: the exec image drops stray caller HERDR_* names — only
// the marker's routing may steer the client — while the printed command can
// only prefix the routing values and inherits whatever the invoking shell
// still exports.
func (s *OwnedSession) AttachForms(baseEnvironment []string) (string, corebackend.AttachExec, error) {
	m, err := s.verifiedAttachMarker()
	if err != nil {
		return "", corebackend.AttachExec{}, err
	}
	assignments := attachAssignments(m)
	spec := corebackend.AttachExec{
		Path: m.BinaryPath,
		Argv: []string{m.BinaryPath},
		Env:  mergeAttachEnvironment(baseEnvironment, assignments),
	}
	return renderAttachCommand(m, assignments), spec, nil
}

func renderAttachCommand(m ownerMarker, assignments [][2]string) string {
	parts := make([]string, 0, len(assignments)+1)
	for _, assignment := range assignments {
		parts = append(parts, assignment[0]+"="+shellQuote(assignment[1]))
	}
	return strings.Join(append(parts, shellQuote(m.BinaryPath)), " ")
}

// verifiedAttachMarker admits the owned session and proves it is alive before
// anything hands its routing to a terminal, mirroring every other owned
// operation's admission.
func (s *OwnedSession) verifiedAttachMarker() (ownerMarker, error) {
	if s == nil || s.backend == nil {
		return ownerMarker{}, fmt.Errorf("herdr owned session is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*commandTimeout)
	defer cancel()
	var marker ownerMarker
	wrap := ownedErrors{probe: func(err error) error { return fmt.Errorf("verify herdr owned session before attach: %w", err) }}
	err := s.backend.withOwned(ctx, ownedOperationLane, wrap, func(call ownedCall) error {
		marker = call.admission.marker
		return nil
	})
	if err != nil {
		return ownerMarker{}, err
	}
	return marker, nil
}

func attachAssignments(m ownerMarker) [][2]string {
	return [][2]string{
		{xdgConfigEnv, m.XDGConfigHome},
		{xdgStateEnv, m.XDGStateHome},
		{xdgDataEnv, m.XDGDataHome},
		{xdgCacheEnv, m.XDGCacheHome},
		{configEnv, m.ConfigPath},
		{sessionEnv, m.Session},
		{socketEnv, m.SocketPath},
		{clientSocketEnv, m.ClientSocketPath},
	}
}

// mergeAttachEnvironment carries the caller environment into the exec image
// with the owned routing appended last, replacing same-named entries and
// dropping stray HERDR_* names. Every other entry passes through verbatim,
// malformed ones included, exactly as pasting the printed command would
// inherit them — the client is the runtime's own pinned binary, not a fanout
// workload, so the strict capsule filter (blockedCallerEnvironmentName) is
// deliberately not applied here.
func mergeAttachEnvironment(base []string, assignments [][2]string) []string {
	overridden := make(map[string]bool, len(assignments))
	for _, assignment := range assignments {
		overridden[assignment[0]] = true
	}
	merged := make([]string, 0, len(base)+len(assignments))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (overridden[name] || strings.HasPrefix(name, "HERDR_")) {
			continue
		}
		merged = append(merged, entry)
	}
	for _, assignment := range assignments {
		merged = append(merged, assignment[0]+"="+assignment[1])
	}
	return merged
}

func ownedSessionFromMarker(
	commonDir string,
	marker ownerMarker,
	emitterPath string,
	backend *Backend,
) *OwnedSession {
	return &OwnedSession{
		Session: marker.Session, SocketPath: marker.SocketPath,
		ClientSocketPath: marker.ClientSocketPath, GitCommonDir: commonDir,
		RuntimeDir: marker.RuntimeDir, LauncherPath: marker.LauncherPath,
		EmitterPath: emitterPath,
		ControlPath: filepath.Join(commonDir, "fanout", "herdr-intents.json"), backend: backend,
	}
}

func currentOwnedEmitterPath(marker ownerMarker) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return ""
	}
	current, err := os.Open(executable)
	if err != nil {
		return ""
	}
	defer func() { _ = current.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, current); err != nil {
		return ""
	}
	if hex.EncodeToString(hash.Sum(nil)) == marker.LauncherSHA256 {
		return marker.LauncherPath
	}
	return filepath.Clean(executable)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
