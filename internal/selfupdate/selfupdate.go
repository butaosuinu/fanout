// Package selfupdate compares the running fanout version with release tags and
// drives release updates through install.sh.
package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const InstallScriptURL = "https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh"

type Outcome int

const (
	DevBuild Outcome = iota
	UpToDate
	UpdateAvailable
	CurrentAhead
	CannotCompare
)

func (o Outcome) String() string {
	switch o {
	case DevBuild:
		return "dev build"
	case UpToDate:
		return "up to date"
	case UpdateAvailable:
		return "update available"
	case CurrentAhead:
		return "current ahead"
	case CannotCompare:
		return "cannot compare"
	default:
		return "unknown"
	}
}

// Compare returns the relationship between current and latest release tags.
// It accepts exactly MAJOR.MINOR.PATCH, optionally prefixed with "v".
func Compare(current, latest string) Outcome {
	if strings.TrimSpace(current) == "dev" {
		return DevBuild
	}

	cur, ok := parseVersion(current)
	if !ok {
		return CannotCompare
	}
	rel, ok := parseVersion(latest)
	if !ok {
		return CannotCompare
	}

	for i := range cur {
		switch {
		case cur[i] < rel[i]:
			return UpdateAvailable
		case cur[i] > rel[i]:
			return CurrentAhead
		}
	}
	return UpToDate
}

const UsageText = `Usage: fanout update [--version <tag>] [--no-skills]

Updates this fanout binary and bundled Claude/Codex integrations by reusing
the release install.sh script.

Options:
  --version <tag>  Install a pinned release tag, e.g. v0.2.0.
  --no-skills      Update only the fanout binary.
  -h, --help       Show this help.
`

type FailureKind int

const (
	FailureEnv FailureKind = iota
	FailureInvocation
	FailureGitHub
)

type Failure struct {
	Kind FailureKind
	Err  error
}

func (f *Failure) Error() string {
	if f == nil || f.Err == nil {
		return ""
	}
	return f.Err.Error()
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

func fail(kind FailureKind, format string, args ...any) error {
	return &Failure{Kind: kind, Err: fmt.Errorf(format, args...)}
}

func Kind(err error) (FailureKind, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f.Kind, true
	}
	return FailureEnv, false
}

type Request struct {
	NoSkills bool
	Version  string
	Help     bool
}

func ParseArgs(args []string) (Request, error) {
	var req Request
	for i := 0; i < len(args); {
		switch arg := args[i]; arg {
		case "--no-skills":
			req.NoSkills = true
			i++
		case "--version":
			if i+1 >= len(args) {
				return req, fail(FailureEnv, "--version requires an argument")
			}
			req.Version = args[i+1]
			i += 2
		case "-h", "--help":
			req.Help = true
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				return req, fail(FailureInvocation, "unknown update option: %s", arg)
			}
			return req, fail(FailureInvocation, "unexpected update argument: %s", arg)
		}
	}
	return req, nil
}

type Plan struct {
	CurrentVersion string
	TargetVersion  string
	TargetSource   string
	ExecutablePath string
	ResolvedPath   string
	BinDir         string
	NoSkills       bool
	Outcome        Outcome
	InstallerURL   string
	ExplicitTarget bool
}

func (p Plan) NeedsInstaller() bool {
	switch p.Outcome {
	case UpToDate:
		return p.ExplicitTarget
	case UpdateAvailable:
		return true
	case CurrentAhead:
		return p.ExplicitTarget
	default:
		return false
	}
}

func (p Plan) UnsupportedExecutableName() bool {
	return filepath.Base(p.ResolvedPath) != "fanout"
}

type Options struct {
	CurrentVersion string
	Request        Request
	LatestTag      func() (string, error)
	Runner         CommandRunner
	Stdout         io.Writer
	Stderr         io.Writer
	Stdin          io.Reader

	Executable   func() (string, error)
	EvalSymlinks func(string) (string, error)
	LookPath     func(string) (string, error)
	WritableDir  func(string) error
}

func Run(opts Options) (Plan, error) {
	opts = withDefaults(opts)
	plan, err := ResolvePlan(opts)
	if err != nil {
		return plan, err
	}

	if plan.Outcome == DevBuild {
		return plan, fail(FailureEnv, "fanout dev build: update only replaces released versions")
	}
	if plan.Outcome == CannotCompare {
		return plan, fail(FailureInvocation, "cannot compare current version %q with target release %q", plan.CurrentVersion, plan.TargetVersion)
	}
	if !plan.NeedsInstaller() {
		PrintNoop(opts.Stdout, plan)
		return plan, nil
	}
	if plan.UnsupportedExecutableName() {
		return plan, fail(FailureEnv, "update can only replace an executable named fanout; current executable is %s", plan.ResolvedPath)
	}

	if err := requireDownloader(opts.LookPath); err != nil {
		return plan, err
	}
	if err := opts.WritableDir(plan.BinDir); err != nil {
		return plan, fail(FailureEnv, "cannot write to binary directory %s: %w", plan.BinDir, err)
	}

	PrintUpdatePlan(opts.Stdout, plan)

	if err := opts.Runner.Run(BuildInstallerCommand(plan)); err != nil {
		return plan, fail(FailureEnv, "install.sh failed: %w", err)
	}
	return plan, nil
}

func ResolvePlan(opts Options) (Plan, error) {
	opts = withDefaults(opts)
	req := opts.Request

	exe, err := opts.Executable()
	if err != nil {
		return Plan{}, fail(FailureEnv, "cannot locate current executable: %w", err)
	}
	resolved, err := opts.EvalSymlinks(exe)
	if err != nil {
		return Plan{}, fail(FailureEnv, "cannot resolve current executable %s: %w", exe, err)
	}

	plan := Plan{
		CurrentVersion: opts.CurrentVersion,
		ExecutablePath: exe,
		ResolvedPath:   resolved,
		BinDir:         filepath.Dir(resolved),
		NoSkills:       req.NoSkills,
		InstallerURL:   InstallScriptURL,
	}

	switch {
	case req.Version != "":
		plan.TargetSource = "pinned"
		plan.ExplicitTarget = true
		if _, ok := parseVersion(req.Version); !ok {
			plan.TargetVersion = req.Version
			plan.Outcome = CannotCompare
			return plan, nil
		}
		plan.TargetVersion = normalizeReleaseTag(req.Version)
		if Compare(opts.CurrentVersion, "") == DevBuild {
			plan.Outcome = DevBuild
			return plan, nil
		}
	case Compare(opts.CurrentVersion, "") == DevBuild:
		plan.TargetVersion = "latest"
		plan.TargetSource = "latest"
		plan.Outcome = DevBuild
		return plan, nil
	default:
		latest, err := opts.LatestTag()
		if err != nil {
			return plan, fail(FailureGitHub, "failed to fetch latest release tag: %w", err)
		}
		plan.TargetVersion = latest
		plan.TargetSource = "latest"
	}

	plan.Outcome = Compare(opts.CurrentVersion, plan.TargetVersion)
	return plan, nil
}

func PrintUpdatePlan(w io.Writer, plan Plan) {
	printPlan(w, "fanout update", plan)
	fmt.Fprintf(w, "  action:          run install.sh\n")
}

func PrintNoop(w io.Writer, plan Plan) {
	switch plan.Outcome {
	case UpToDate:
		fmt.Fprintf(w, "fanout is already up to date: %s\n", plan.CurrentVersion)
	case CurrentAhead:
		fmt.Fprintf(w, "fanout %s is newer than latest release %s\n", plan.CurrentVersion, plan.TargetVersion)
	default:
		fmt.Fprintf(w, "fanout update: nothing to do (%s)\n", plan.Outcome)
	}
}

func printPlan(w io.Writer, title string, plan Plan) {
	fmt.Fprintln(w, title)
	fmt.Fprintf(w, "  current version: %s\n", plan.CurrentVersion)
	fmt.Fprintf(w, "  target version:  %s (%s)\n", plan.TargetVersion, plan.TargetSource)
	fmt.Fprintf(w, "  current binary:  %s\n", plan.ResolvedPath)
	if plan.ExecutablePath != plan.ResolvedPath {
		fmt.Fprintf(w, "  invoked path:    %s\n", plan.ExecutablePath)
	}
	fmt.Fprintf(w, "  binary dir:      %s\n", plan.BinDir)
	fmt.Fprintf(w, "  install script:  %s\n", plan.InstallerURL)
	fmt.Fprintf(w, "  skills:          %s\n", yesNo(!plan.NoSkills))
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func normalizeReleaseTag(version string) string {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

type CommandSpec struct {
	Name string
	Args []string
	Env  []string
}

type CommandRunner interface {
	Run(CommandSpec) error
}

type CommandRunnerFunc func(CommandSpec) error

func (f CommandRunnerFunc) Run(spec CommandSpec) error {
	return f(spec)
}

type ExecRunner struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Environ func() []string
}

func (r ExecRunner) Run(spec CommandSpec) error {
	cmd := exec.Command(spec.Name, spec.Args...)
	if r.Stdin != nil {
		cmd.Stdin = r.Stdin
	}
	if r.Stdout != nil {
		cmd.Stdout = r.Stdout
	}
	if r.Stderr != nil {
		cmd.Stderr = r.Stderr
	}
	environ := os.Environ
	if r.Environ != nil {
		environ = r.Environ
	}
	cmd.Env = append(environ(), spec.Env...)
	return cmd.Run()
}

func BuildInstallerCommand(plan Plan) CommandSpec {
	args := []string{
		"-c",
		installerShellScript,
		"fanout-update",
		plan.InstallerURL,
	}
	if plan.NoSkills {
		args = append(args, "--no-skills")
	}
	return CommandSpec{
		Name: "sh",
		Args: args,
		Env: []string{
			"BIN_DIR=" + plan.BinDir,
			"FANOUT_VERSION=" + plan.TargetVersion,
		},
	}
}

const installerShellScript = `set -eu
url=$1
shift
script=$(mktemp "${TMPDIR:-/tmp}/fanout-install.XXXXXX")
trap 'rm -f "$script"' EXIT HUP INT TERM
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$script"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$script" "$url"
else
  echo "fanout update: curl or wget is required to fetch install.sh" >&2
  exit 1
fi
sh "$script" "$@"`

func withDefaults(opts Options) Options {
	if opts.CurrentVersion == "" {
		opts.CurrentVersion = "dev"
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader("")
	}
	if opts.LatestTag == nil {
		opts.LatestTag = func() (string, error) {
			return "", errors.New("latest release resolver is not configured")
		}
	}
	if opts.Executable == nil {
		opts.Executable = os.Executable
	}
	if opts.EvalSymlinks == nil {
		opts.EvalSymlinks = filepath.EvalSymlinks
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.WritableDir == nil {
		opts.WritableDir = checkWritableDir
	}
	if opts.Runner == nil {
		opts.Runner = ExecRunner{Stdin: opts.Stdin, Stdout: opts.Stdout, Stderr: opts.Stderr}
	}
	return opts
}

func requireDownloader(lookPath func(string) (string, error)) error {
	if _, err := lookPath("curl"); err == nil {
		return nil
	}
	if _, err := lookPath("wget"); err == nil {
		return nil
	}
	return fail(FailureEnv, "curl or wget is required to fetch install.sh")
}

func checkWritableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".fanout-update-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if part == "" {
			return out, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return out, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
