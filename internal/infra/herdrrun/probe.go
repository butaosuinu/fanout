package herdrrun

import (
	"context"
	"fmt"
	"path/filepath"
)

type probeResult struct {
	binary  string
	sha256  string
	version string
	route   route
}

type binaryAdmission struct {
	path    string
	sha256  string
	version string
}

func (b *herdrCLI) probe() (probeResult, error) {
	return b.probeContext(context.Background())
}

func (b *herdrCLI) probeContext(ctx context.Context) (probeResult, error) {
	select {
	case b.probeGate <- struct{}{}:
		defer func() { <-b.probeGate }()
	case <-ctx.Done():
		return probeResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return probeResult{}, err
	}

	if err := validateSessionName(b.session); err != nil {
		return probeResult{}, err
	}
	initial := route{session: b.session, socketPath: b.socketPath}
	admitted, err := b.admitBinaryContext(ctx, initial)
	if err != nil {
		return probeResult{}, err
	}

	statusArgs := []string{"status", "--json"}
	// Use --session only to discover an external named session. Once a
	// socket is known, every call selects it explicitly because HERDR_SOCKET_PATH
	// takes precedence over HERDR_SESSION.
	if initial.socketPath == "" {
		statusArgs = append([]string{"--session", initial.session}, statusArgs...)
	}
	statusOut, err := b.runReadContext(ctx, admitted.path, initial, statusArgs...)
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr status --json: %w", err)
	}
	var status statusJSON
	if decodeErr := decodeOne(statusOut, &status); decodeErr != nil {
		return probeResult{}, fmt.Errorf("parse herdr status --json: %w", decodeErr)
	}
	verified, err := validateStatus(status, initial, admitted)
	if err != nil {
		return probeResult{}, err
	}
	if b.socketPath == "" {
		b.socketPath = verified.socketPath
	}
	return probeResult{
		binary:  admitted.path,
		sha256:  admitted.sha256,
		version: admitted.version,
		route:   verified,
	}, nil
}

func (b *herdrCLI) admitBinaryContext(ctx context.Context, target route) (binaryAdmission, error) {
	binary, err := b.lookPath(commandName)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr stable >=%s is required: %w", minimumVersion, err)
	}
	if !filepath.IsAbs(binary) {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return binaryAdmission{}, fmt.Errorf("resolve herdr executable: %w", err)
		}
	}
	binary, hash, err := b.stageBinary(binary)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("stage herdr executable before admission: %w", err)
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || !validHexToken(hash) {
		return binaryAdmission{}, fmt.Errorf("staged herdr executable has an invalid content identity")
	}
	versionOut, err := b.runContext(ctx, commandTimeout, binary, target, "--version")
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr --version: %w", err)
	}
	version, err := parseAdmittedVersion(versionOut)
	if err != nil {
		return binaryAdmission{}, err
	}
	admitted := binaryAdmission{path: binary, sha256: hash, version: version}
	key := binary + "\x00" + hash
	if cached, ok := b.admitted[key]; ok {
		if cached != admitted {
			return binaryAdmission{}, fmt.Errorf("herdr admitted binary identity changed")
		}
		return cached, nil
	}
	b.admitted[key] = admitted
	return admitted, nil
}
