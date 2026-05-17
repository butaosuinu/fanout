package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/dmuxsession"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

func TestExecutePlanSleepsBetweenDryRunIssues(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "dmux.config.json")
	if err := os.WriteFile(configPath, []byte(`{"panes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldSleep := sleepBetweenIssues
	var sleeps []time.Duration
	sleepBetweenIssues = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}
	t.Cleanup(func() { sleepBetweenIssues = oldSleep })

	cfg := &cliflags.Config{
		Agent:        "claude",
		DryRun:       true,
		SleepBetween: 0.25,
	}
	lg := log.NewWith(io.Discard, io.Discard, false)
	info := &dmuxsession.Info{
		ControlPane: "%1",
		ConfigPath:  configPath,
		ProjectRoot: dir,
	}
	targets := []ghissue.Issue{
		{Number: 1, Title: "one", State: "OPEN", Body: "body"},
		{Number: 2, Title: "two", State: "OPEN", Body: "body"},
	}

	result := executePlan(cfg, lg, info, ghissue.Runner{}, targets, log.Palette{})

	if result.Created != 2 || result.Failed != 0 {
		t.Fatalf("executePlan result = %+v, want 2 created and 0 failed", result)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls = %d, want 1", len(sleeps))
	}
	if want := 250 * time.Millisecond; sleeps[0] != want {
		t.Fatalf("sleep duration = %s, want %s", sleeps[0], want)
	}
}
