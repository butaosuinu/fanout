package selfupdate

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		latest  string
		want    Outcome
	}{
		{name: "dev build", current: "dev", latest: "v9.9.9", want: DevBuild},
		{name: "equal tags", current: "v1.2.3", latest: "v1.2.3", want: UpToDate},
		{name: "equal without v prefix", current: "1.2.3", latest: "v1.2.3", want: UpToDate},
		{name: "latest patch ahead", current: "v1.2.3", latest: "v1.2.4", want: UpdateAvailable},
		{name: "latest minor ahead", current: "v1.2.9", latest: "v1.3.0", want: UpdateAvailable},
		{name: "latest major ahead", current: "v1.9.9", latest: "v2.0.0", want: UpdateAvailable},
		{name: "current ahead", current: "v2.0.0", latest: "v1.9.9", want: CurrentAhead},
		{name: "invalid current", current: "release", latest: "v1.2.3", want: CannotCompare},
		{name: "invalid latest", current: "v1.2.3", latest: "v1.2.x", want: CannotCompare},
		{name: "fewer components", current: "v1.2", latest: "v1.2.0", want: CannotCompare},
		{name: "more components", current: "v1.2.0", latest: "v1.2.0.1", want: CannotCompare},
		{name: "prerelease rejected", current: "v1.2.3", latest: "v1.2.4-beta.1", want: CannotCompare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compare(tc.current, tc.latest); got != tc.want {
				t.Fatalf("Compare(%q, %q) = %s, want %s", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func TestBuildInstallerCommandMapsVersionEnvAndNoSkills(t *testing.T) {
	spec := BuildInstallerCommand(Plan{
		BinDir:        "/opt/fanout/bin",
		TargetVersion: "v9.8.7",
		NoSkills:      true,
		InstallerURL:  InstallScriptURL,
	})

	if spec.Name != "sh" {
		t.Fatalf("command name = %q, want sh", spec.Name)
	}
	if len(spec.Args) != 5 {
		t.Fatalf("args = %#v, want shell script plus URL and --no-skills", spec.Args)
	}
	if spec.Args[0] != "-c" {
		t.Fatalf("args[0] = %q, want -c", spec.Args[0])
	}
	if spec.Args[2] != "fanout-update" {
		t.Fatalf("args[2] = %q, want fanout-update", spec.Args[2])
	}
	if spec.Args[3] != InstallScriptURL {
		t.Fatalf("installer URL = %q, want %q", spec.Args[3], InstallScriptURL)
	}
	if spec.Args[4] != "--no-skills" {
		t.Fatalf("last arg = %q, want --no-skills", spec.Args[4])
	}
	if strings.Contains(spec.Args[1], "| sh") {
		t.Fatalf("installer script pipes downloader into sh, which hides download failures:\n%s", spec.Args[1])
	}
	for _, want := range []string{
		`curl -fsSL "$url" -o "$script"`,
		`wget -qO "$script" "$url"`,
		`sh "$script" "$@"`,
	} {
		if !strings.Contains(spec.Args[1], want) {
			t.Fatalf("installer script missing %q:\n%s", want, spec.Args[1])
		}
	}

	env := envMap(spec.Env)
	if got, want := env["BIN_DIR"], "/opt/fanout/bin"; got != want {
		t.Fatalf("BIN_DIR = %q, want %q", got, want)
	}
	if got, want := env["FANOUT_VERSION"], "v9.8.7"; got != want {
		t.Fatalf("FANOUT_VERSION = %q, want %q", got, want)
	}
}

func TestRunUpdatePrintsPlanAndRunsInstaller(t *testing.T) {
	var out bytes.Buffer
	called := false
	plan, err := Run(testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		called = true
		return nil
	})))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("installer runner was not called")
	}
	if plan.TargetVersion != "v0.2.0" || plan.Outcome != UpdateAvailable {
		t.Fatalf("plan = %+v, want target v0.2.0 update available", plan)
	}
	got := out.String()
	for _, want := range []string{
		"fanout update",
		"current version: v0.1.0",
		"target version:  v0.2.0 (latest)",
		"current binary:  /tmp/fanout-real/fanout",
		"action:          run install.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q:\n%s", want, got)
		}
	}
}

func TestRunRejectsIncomparablePinnedVersion(t *testing.T) {
	var out bytes.Buffer
	_, err := Run(testOptions(Request{Version: "not-semver"}, &out, CommandRunnerFunc(func(CommandSpec) error {
		t.Fatal("installer runner should not be called")
		return nil
	})))
	if err == nil {
		t.Fatal("Run returned nil error, want incomparable-version failure")
	}
	assertFailureKind(t, err, FailureInvocation)
	if !strings.Contains(err.Error(), "cannot compare current version") {
		t.Fatalf("error = %v, want compare message", err)
	}
}

func TestRunPinnedVersionPassesFANOUTVersionAndNoSkillsToInstaller(t *testing.T) {
	var got CommandSpec
	var out bytes.Buffer
	plan, err := Run(testOptions(Request{Version: "9.9.9", NoSkills: true}, &out, CommandRunnerFunc(func(spec CommandSpec) error {
		got = spec
		return nil
	})))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got, want := plan.TargetVersion, "v9.9.9"; got != want {
		t.Fatalf("TargetVersion = %q, want %q", got, want)
	}

	env := envMap(got.Env)
	if got, want := env["BIN_DIR"], "/tmp/fanout-real"; got != want {
		t.Fatalf("BIN_DIR = %q, want %q", got, want)
	}
	if got, want := env["FANOUT_VERSION"], "v9.9.9"; got != want {
		t.Fatalf("FANOUT_VERSION = %q, want %q", got, want)
	}
	if got.Name != "sh" {
		t.Fatalf("command name = %q, want sh", got.Name)
	}
	if got.Args[len(got.Args)-1] != "--no-skills" {
		t.Fatalf("args = %#v, want --no-skills forwarded", got.Args)
	}
}

func TestRunPinnedCurrentVersionStillRunsInstaller(t *testing.T) {
	var got CommandSpec
	var out bytes.Buffer
	opts := testOptions(Request{Version: "0.1.0"}, &out, CommandRunnerFunc(func(spec CommandSpec) error {
		got = spec
		return nil
	}))
	opts.LatestTag = func() (string, error) {
		t.Fatal("latest release lookup should not be called for pinned version")
		return "", nil
	}

	plan, err := Run(opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if plan.Outcome != UpToDate || !plan.NeedsInstaller() {
		t.Fatalf("plan = %+v, want up-to-date explicit target that needs installer", plan)
	}
	env := envMap(got.Env)
	if got, want := env["FANOUT_VERSION"], "v0.1.0"; got != want {
		t.Fatalf("FANOUT_VERSION = %q, want %q", got, want)
	}
	if got.Name != "sh" {
		t.Fatalf("command name = %q, want sh", got.Name)
	}
}

func TestRunDevBuildRejectsReplacementBeforeLookupOrInstaller(t *testing.T) {
	var out bytes.Buffer
	latestCalled := false
	runnerCalled := false
	opts := testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		runnerCalled = true
		return nil
	}))
	opts.CurrentVersion = "dev"
	opts.LatestTag = func() (string, error) {
		latestCalled = true
		return "v0.2.0", nil
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("Run returned nil error, want dev-build failure")
	}
	assertFailureKind(t, err, FailureEnv)
	if latestCalled {
		t.Fatal("latest release lookup was called for dev replacement")
	}
	if runnerCalled {
		t.Fatal("installer runner was called for dev replacement")
	}
}

func TestRunUpToDateSkipsInstaller(t *testing.T) {
	var out bytes.Buffer
	called := false
	opts := testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		called = true
		return nil
	}))
	opts.CurrentVersion = "v0.2.0"

	_, err := Run(opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("installer runner was called for up-to-date version")
	}
	if got := out.String(); !strings.Contains(got, "fanout is already up to date: v0.2.0") {
		t.Fatalf("output = %q, want already up-to-date message", got)
	}
}

func TestRunRejectsNonFanoutExecutableName(t *testing.T) {
	var out bytes.Buffer
	opts := testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		t.Fatal("installer runner should not be called")
		return nil
	}))
	opts.EvalSymlinks = func(string) (string, error) {
		return "/tmp/fanout-real/fanout-v0.1.0", nil
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("Run returned nil error, want executable-name failure")
	}
	assertFailureKind(t, err, FailureEnv)
	if !strings.Contains(err.Error(), "executable named fanout") {
		t.Fatalf("error = %v, want executable-name message", err)
	}
}

func TestRunRequiresDownloader(t *testing.T) {
	var out bytes.Buffer
	opts := testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		t.Fatal("installer runner should not be called")
		return nil
	}))
	opts.LookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("Run returned nil error, want downloader failure")
	}
	assertFailureKind(t, err, FailureEnv)
	if !strings.Contains(err.Error(), "curl or wget is required") {
		t.Fatalf("error = %v, want curl/wget message", err)
	}
}

func TestRunRejectsUnwritableBinDir(t *testing.T) {
	var out bytes.Buffer
	opts := testOptions(Request{}, &out, CommandRunnerFunc(func(CommandSpec) error {
		t.Fatal("installer runner should not be called")
		return nil
	}))
	opts.WritableDir = func(string) error {
		return errors.New("permission denied")
	}

	_, err := Run(opts)
	if err == nil {
		t.Fatal("Run returned nil error, want writable-dir failure")
	}
	assertFailureKind(t, err, FailureEnv)
	if !strings.Contains(err.Error(), "cannot write to binary directory /tmp/fanout-real") {
		t.Fatalf("error = %v, want binary directory message", err)
	}
}

func testOptions(req Request, out *bytes.Buffer, runner CommandRunner) Options {
	return Options{
		CurrentVersion: "v0.1.0",
		Request:        req,
		LatestTag: func() (string, error) {
			return "v0.2.0", nil
		},
		Runner: runner,
		Stdout: out,
		Stderr: out,
		Stdin:  strings.NewReader(""),
		Executable: func() (string, error) {
			return "/tmp/fanout-link/fanout", nil
		},
		EvalSymlinks: func(string) (string, error) {
			return "/tmp/fanout-real/fanout", nil
		},
		LookPath: func(string) (string, error) {
			return "/usr/bin/downloader", nil
		},
		WritableDir: func(string) error {
			return nil
		},
	}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, pair := range env {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func assertFailureKind(t *testing.T, err error, want FailureKind) {
	t.Helper()
	got, ok := Kind(err)
	if !ok {
		t.Fatalf("Kind(%v) not available", err)
	}
	if got != want {
		t.Fatalf("Kind(%v) = %v, want %v", err, got, want)
	}
}
