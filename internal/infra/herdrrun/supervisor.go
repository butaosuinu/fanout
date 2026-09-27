package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type startedSupervisor struct {
	pid                     int
	dashboardAuthentication dashboardAuthentication
	signal                  func(os.Signal) error
	wait                    func() error
}

func (s *startedSupervisor) reapAsync() {
	if s == nil || s.wait == nil {
		return
	}
	go func() {
		// Successful readiness hands terminal cleanup to the supervisor lifecycle.
		_ = s.wait()
	}()
}

func startOwnedSupervisor(markerPath, nonce, startToken string) (*startedSupervisor, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	cmd, authentication := newOwnedSupervisorCommand(exe, markerPath, nonce, startToken)
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Dir = filepath.Dir(markerPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logFile, err := openPrivateAppendFile(filepath.Join(filepath.Dir(markerPath), ownedSupervisorLogName))
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = logFile.Close()
		return nil, err
	}
	_ = writer.Close()
	_ = logFile.Close()
	if err := reader.SetReadDeadline(time.Now().Add(ownedReadyTimeout)); err != nil {
		stopStartedOwnedCommand(cmd)
		return nil, err
	}
	one := []byte{0}
	if _, err := io.ReadFull(reader, one); err != nil || string(one) != ownedSupervisorReadyACK {
		stopStartedOwnedCommand(cmd)
		return nil, fmt.Errorf("herdr supervisor readiness handshake failed")
	}
	return &startedSupervisor{
		pid:                     cmd.Process.Pid,
		dashboardAuthentication: authentication,
		signal:                  cmd.Process.Signal,
		wait:                    cmd.Wait,
	}, nil
}

func newOwnedSupervisorCommand(
	exe, markerPath, nonce, startToken string,
) (*exec.Cmd, dashboardAuthentication) {
	hostEnvironment := os.Environ()
	cmd := exec.Command(exe, ownedSupervisorCommand, markerPath, nonce, startToken, strconv.Itoa(ownedSupervisorReadyFD))
	cmd.Env = dashboardSupervisorEnvironment(hostEnvironment)
	return cmd, dashboardAuthenticationFromCaller(hostEnvironment)
}

func stopStartedOwnedCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Wait below observes the definitive process state after this best-effort kill.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func IsSupervisorRequest(args []string) bool {
	return len(args) > 0 && args[0] == ownedSupervisorCommand
}

//nolint:funlen // The supervisor owns one process and keeps its startup and shutdown fencing in one scope.
func RunSupervisor(args []string, errw io.Writer) int {
	if len(args) != 4 {
		fmt.Fprintln(errw, "fanout herdr supervisor: expected marker path, nonce, start token, and ready fd")
		return 2
	}
	markerPath, nonce, startToken := args[0], args[1], args[2]
	readyFD, err := strconv.Atoi(args[3])
	if err != nil || readyFD != ownedSupervisorReadyFD || !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath ||
		filepath.Base(markerPath) != ownedMarkerName || !validHexToken(nonce) || !validHexToken(startToken) {
		fmt.Fprintln(errw, "fanout herdr supervisor: invalid marker path, nonce, start token, or ready fd")
		return 2
	}
	ready := os.NewFile(uintptr(readyFD), "herdr-supervisor-ready")
	if ready == nil {
		fmt.Fprintln(errw, "fanout herdr supervisor: invalid ready fd")
		return 2
	}
	defer func() { _ = ready.Close() }()
	runtimeDir := filepath.Dir(markerPath)
	err = ensurePrivateDir(runtimeDir)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: runtime directory: %v\n", err)
		return 1
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, ownedSupervisorLockName), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		var info os.FileInfo
		info, err = lock.Stat()
		if err == nil {
			err = validatePrivateRegular(filepath.Join(runtimeDir, ownedSupervisorLockName), info)
		}
	}
	if err != nil || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Fprintln(errw, "fanout herdr supervisor: another supervisor owns this session")
		if lock != nil {
			_ = lock.Close()
		}
		return 1
	}
	defer unlockPrivateFile(lock)
	_, err = ready.Write([]byte(ownedSupervisorReadyACK))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: ready handshake: %v\n", err)
		return 1
	}
	_ = ready.Close()
	var marker ownerMarker
	deadline := time.Now().Add(ownedReadyTimeout)
	for time.Now().Before(deadline) {
		loaded, found, readErr := readOwnerMarker(markerPath)
		if readErr != nil {
			fmt.Fprintf(errw, "fanout herdr supervisor: marker: %v\n", readErr)
			return 1
		}
		if found && loaded.OwnerNonce == nonce && loaded.SupervisorStartToken == startToken && loaded.SupervisorPID == os.Getpid() {
			marker = loaded
			break
		}
		time.Sleep(ownedReadyInterval)
	}
	if marker.OwnerNonce == "" {
		fmt.Fprintln(errw, "fanout herdr supervisor: timed out waiting for ownership marker")
		return 1
	}
	layout, err := prepareOwnedLayout(filepath.Dir(runtimeDir), marker.Session)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: layout: %v\n", err)
		return 1
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(marker.GitCommonDir)
	if err != nil || commonDir != marker.GitCommonDir {
		fmt.Fprintln(errw, "fanout herdr supervisor: git common directory identity mismatch")
		return 1
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	launcher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
	err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: marker identity: %v\n", err)
		return 1
	}
	err = writeSupervisorLease(lock, marker, 0)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: write lease: %v\n", err)
		return 1
	}
	logFile, err := openPrivateAppendFile(filepath.Join(runtimeDir, ownedSupervisorLogName))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: log: %v\n", err)
		return 1
	}
	defer func() { _ = logFile.Close() }()
	_ = syscall.Umask(0o077)
	cmd := exec.Command(marker.BinaryPath, "server")
	cmd.Env = ownedMarkerEnvironment(marker)
	cmd.Dir = runtimeDir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGCHLD)
	defer signal.Stop(signals)
	if err := startOwnedServerWithLease(cmd, lock, marker); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: start server: %v\n", err)
		return 1
	}
	reaped := false
	defer func() {
		if reaped {
			return
		}
		// The leader has not been reaped, so its PID cannot have been reused.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	code := 0
	received := <-signals
	if received == syscall.SIGCHLD {
		waitErr := cmd.Wait()
		reaped = true
		if waitErr != nil {
			code = 1
		}
		// A spontaneous leader exit cannot prove that its descendants are
		// absent. Retain the marker and sockets so the next adoption fails
		// closed instead of mutating a possibly live old namespace.
		return code
	}
	typed, ok := received.(syscall.Signal)
	if !ok {
		fmt.Fprintln(errw, "fanout herdr supervisor: unsupported shutdown signal")
		return 1
	}
	if killErr := syscall.Kill(-cmd.Process.Pid, typed); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		fmt.Fprintf(errw, "fanout herdr supervisor: signal server process group: %v\n", killErr)
		code = 1
	}
	// Keep the leader unreaped during the grace period. This prevents PID/PGID
	// reuse before the final group kill.
	time.Sleep(ownedShutdownGrace)
	if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		fmt.Fprintf(errw, "fanout herdr supervisor: kill server process group: %v\n", killErr)
		code = 1
	}
	if waitErr := cmd.Wait(); waitErr != nil && code == 0 {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			fmt.Fprintf(errw, "fanout herdr supervisor: reap server: %v\n", waitErr)
			code = 1
		}
	}
	reaped = true
	if err := retireOwnedSession(layout, marker, lock); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: retire owned session: %v\n", err)
		return 1
	}
	return code
}

func startOwnedServerWithLease(cmd *exec.Cmd, lock *os.File, marker ownerMarker) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := writeSupervisorLease(lock, marker, cmd.Process.Pid); err != nil {
		// The lease error is authoritative; these calls only ensure the unrecorded
		// child cannot survive the failed ownership publication.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return fmt.Errorf("write server lease: %w", err)
	}
	return nil
}

func retireOwnedSession(layout ownedLayout, marker ownerMarker, supervisorLock *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), ownedReadyTimeout)
	defer cancel()
	lifecycleLock, err := lockPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return fmt.Errorf("lock lifecycle for retirement: %w", err)
	}
	defer unlockPrivateFile(lifecycleLock)

	current, found, err := readOwnerMarker(layout.markerPath)
	if err != nil || !found || current != marker {
		return fmt.Errorf("ownership marker changed before socket cleanup")
	}
	lease, err := readLeaseFromFile(supervisorLock)
	if err != nil || lease.PID != marker.SupervisorPID || lease.OwnerNonce != marker.OwnerNonce || lease.StartToken != marker.SupervisorStartToken {
		return fmt.Errorf("supervisor lease changed before socket cleanup")
	}
	for _, path := range []string{marker.SocketPath, marker.ClientSocketPath} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := validatePrivateSocket(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("herdr owned socket %s still exists after cleanup", path)
		}
	}
	if err := os.Remove(layout.markerPath); err != nil {
		return fmt.Errorf("retire herdr ownership marker: %w", err)
	}
	if _, err := os.Lstat(layout.markerPath); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("verify retired herdr ownership marker: %w", err)
		}
		return fmt.Errorf("herdr ownership marker still exists after retirement")
	}
	return nil
}
