package briefing

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestPRVisualizationDoesNotChangeBriefingYet(t *testing.T) {
	on := settings.Defaults()
	off := on
	off.PRVisualization = false

	got := Render(122, "prVisualization settings", "Issue body", "claude", off)
	want := Render(122, "prVisualization settings", "Issue body", "claude", on)
	if got != want {
		t.Fatal("PRVisualization changed briefing text before visualization injection is implemented")
	}
}
