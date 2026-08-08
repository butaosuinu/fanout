package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitterHandoffsListsOnlyExactLaunchGeneration(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	statePath := Path(repo)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	launchNonce := strings.Repeat("a", 32)
	otherLaunch := strings.Repeat("b", 32)
	events := []struct {
		launch string
		event  string
	}{
		{launch: launchNonce, event: strings.Repeat("1", 32)},
		{launch: launchNonce, event: strings.Repeat("2", 32)},
		{launch: otherLaunch, event: strings.Repeat("3", 32)},
	}
	for _, event := range events {
		path, err := EmitterHandoffPath(statePath, event.launch, event.event)
		if err != nil {
			t.Fatal(err)
		}
		if err := MarkEmitterHandoff(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ClearEmitterHandoff(path) })
	}
	got, err := EmitterHandoffs(statePath, launchNonce)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("EmitterHandoffs() = %#v, want two exact-generation waiters", got)
	}
}
