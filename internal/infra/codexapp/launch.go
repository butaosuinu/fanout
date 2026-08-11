package codexapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/agent"
)

// PlanTUICommand is the hidden fanout subcommand that runs the Plan Mode
// controller inside the child pane.
const PlanTUICommand = "__codex-plan-tui"

// TeamTUICommand is the hidden fanout subcommand that runs the non-Plan team
// message bridge inside a Codex child pane.
const TeamTUICommand = "__codex-team-tui"

// LaunchCommand builds the shell command that starts the Plan Mode controller
// for a fresh prompt.
func LaunchCommand(fanoutPath, codexPath, prompt, statusPath string) string {
	spec := PlanLaunchSpec(fanoutPath, codexPath, prompt, statusPath)
	args := append([]string{spec.Executable}, spec.Args...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = agent.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// PlanLaunchSpec is the non-shell form of LaunchCommand. Runtime backends
// that own the pane process use it without reparsing shell quoting.
func PlanLaunchSpec(fanoutPath, codexPath, prompt, statusPath string) agent.LaunchSpec {
	if strings.TrimSpace(fanoutPath) == "" {
		fanoutPath = "fanout"
	}
	return agent.LaunchSpec{Executable: fanoutPath, Args: []string{
		PlanTUICommand,
		"--codex", codexPath,
		"--prompt", prompt,
		"--status-file", statusPath,
	}}
}

// TeamLaunchCommand builds the shell command that starts the non-Plan Codex
// team bridge. self is an issue number or plan task id; passing it explicitly
// avoids pane-identity detection before the launcher's state row exists.
func TeamLaunchCommand(fanoutPath, codexPath, prompt, self, parent, statusPath string) string {
	spec := TeamLaunchSpec(fanoutPath, codexPath, prompt, self, parent, statusPath)
	args := append([]string{spec.Executable}, spec.Args...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = agent.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// TeamLaunchSpec is the non-shell form of TeamLaunchCommand. Runtime backends
// that own the child process use it without reparsing shell quoting.
func TeamLaunchSpec(fanoutPath, codexPath, prompt, self, parent, statusPath string) agent.LaunchSpec {
	if strings.TrimSpace(fanoutPath) == "" {
		fanoutPath = "fanout"
	}
	return agent.LaunchSpec{Executable: fanoutPath, Args: []string{
		TeamTUICommand,
		"--codex", codexPath,
		"--prompt", prompt,
		"--self", self,
		"--parent", parent,
		"--status-file", statusPath,
	}}
}

// ResumeLaunchCommand builds the shell command that resumes an existing Plan
// Mode thread in a restored pane.
func ResumeLaunchCommand(fanoutPath, codexPath, threadID, sessionID, statusPath string) string {
	if strings.TrimSpace(fanoutPath) == "" {
		fanoutPath = "fanout"
	}
	args := []string{
		fanoutPath,
		PlanTUICommand,
		"--codex", codexPath,
		"--resume-thread-id", threadID,
	}
	if strings.TrimSpace(sessionID) != "" {
		args = append(args, "--resume-session-id", sessionID)
	}
	args = append(args, "--status-file", statusPath)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = agent.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// TeamStatusPath derives the /tmp status file path for one team bridge launch.
// member keeps plan tasks (whose numeric pane number is always zero) distinct.
func TeamStatusPath(projectRoot, member string, dryRun bool) string {
	repo := safeCodexPlanTempPart(filepath.Base(projectRoot))
	member = safeCodexPlanTempPart(member)
	base := fmt.Sprintf("fanout-codex-team-%s-%s", repo, member)
	if dryRun {
		return filepath.Join("/tmp", base+".json")
	}
	unique := fmt.Sprintf("%s-%d-%d", base, os.Getpid(), time.Now().UnixNano())
	return filepath.Join("/tmp", unique+".json")
}

// StatusPath derives the /tmp status file path for one Plan Mode launch. Dry
// runs use a deterministic name so goldens stay stable.
func StatusPath(projectRoot string, issueNum int, dryRun bool) string {
	repo := safeCodexPlanTempPart(filepath.Base(projectRoot))
	base := fmt.Sprintf("fanout-codex-plan-%s-%d", repo, issueNum)
	return uniqueStatusPath(base, dryRun)
}

// TaskStatusPath derives a distinct /tmp status file path for one issue-less
// plan task. Task panes all have numeric issue zero, so the plan and task ids
// provide the stable dry-run identity instead.
func TaskStatusPath(projectRoot, planSlug, taskID string, dryRun bool) string {
	repo := safeCodexPlanTempPart(filepath.Base(projectRoot))
	plan := safeCodexPlanTempPart(planSlug)
	task := safeCodexPlanTempPart(taskID)
	base := fmt.Sprintf("fanout-codex-plan-%s-%s-%s", repo, plan, task)
	return uniqueStatusPath(base, dryRun)
}

func uniqueStatusPath(base string, dryRun bool) string {
	if dryRun {
		return filepath.Join("/tmp", base+".json")
	}
	unique := fmt.Sprintf("%s-%d-%d", base, os.Getpid(), time.Now().UnixNano())
	return filepath.Join("/tmp", unique+".json")
}

func safeCodexPlanTempPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() == 0 {
		return "repo"
	}
	return b.String()
}
