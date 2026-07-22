package herdrrun

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const admissionBinaryDirName = "admission-binaries"

// stageAdmissionBinary fixes the executable bytes before the first admission
// command. No pathname discovered through PATH is ever executed directly.
func stageAdmissionBinary(sourcePath string) (string, string, error) {
	parent, err := filepath.EvalSymlinks(defaultRuntimeParent)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize herdr admission parent: %w", err)
	}
	runtimeBase := filepath.Join(parent, "fhr-"+strconv.Itoa(os.Getuid()))
	if err := ensurePrivateDir(runtimeBase); err != nil {
		return "", "", fmt.Errorf("prepare herdr admission base: %w", err)
	}
	binaryDir := filepath.Join(runtimeBase, admissionBinaryDirName)
	if err := ensurePrivateDir(binaryDir); err != nil {
		return "", "", fmt.Errorf("prepare herdr admission binary directory: %w", err)
	}
	return stageExecutable(sourcePath, binaryDir)
}

func stageExecutable(sourcePath, binaryDir string) (string, string, error) {
	if err := validatePrivateDir(binaryDir); err != nil {
		return "", "", err
	}
	resolved, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize herdr executable: %w", err)
	}
	if !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return "", "", fmt.Errorf("herdr executable did not resolve to a canonical path")
	}
	source, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", fmt.Errorf("open herdr executable without following links: %w", err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", fmt.Errorf("herdr executable is not a regular executable")
	}
	err = validateOwnerUID(resolved, info)
	if err != nil {
		return "", "", err
	}

	temporary, err := os.CreateTemp(binaryDir, ".herdr-stage-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("create herdr executable stage: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(temporary, hash), source)
	if err != nil {
		return "", "", fmt.Errorf("copy herdr executable stage: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	err = temporary.Sync()
	if err != nil {
		return "", "", fmt.Errorf("sync herdr executable stage: %w", err)
	}
	err = temporary.Chmod(0o500)
	if err != nil {
		return "", "", fmt.Errorf("seal herdr executable stage: %w", err)
	}
	err = temporary.Sync()
	if err != nil {
		return "", "", fmt.Errorf("sync sealed herdr executable stage: %w", err)
	}
	err = temporary.Close()
	if err != nil {
		return "", "", fmt.Errorf("close herdr executable stage: %w", err)
	}

	target := filepath.Join(binaryDir, "herdr-"+digest)
	validationErr := validatePinnedBinaryInDir(target, digest, binaryDir)
	if validationErr == nil {
		return target, digest, nil
	}
	if _, statErr := os.Lstat(target); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("validate existing herdr executable stage: %w", validationErr)
	}
	err = os.Link(temporaryPath, target)
	if errors.Is(err, os.ErrExist) {
		validationErr = validatePinnedBinaryInDir(target, digest, binaryDir)
		if validationErr == nil {
			return target, digest, nil
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("publish herdr executable stage: %w", err)
	}
	err = os.Remove(temporaryPath)
	if err != nil {
		_ = os.Remove(target)
		return "", "", fmt.Errorf("seal herdr executable link identity: %w", err)
	}
	temporaryPath = ""
	dir, err := os.OpenFile(binaryDir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	err = errors.Join(syncErr, closeErr)
	if err != nil {
		return "", "", fmt.Errorf("sync herdr executable stage directory: %w", err)
	}
	err = validatePinnedBinaryInDir(target, digest, binaryDir)
	if err != nil {
		return "", "", err
	}
	return target, digest, nil
}
