package blockers

import (
	"reflect"
	"testing"
)

func TestFromChildBodyStopsAtBlankLineOrHeading(t *testing.T) {
	body := "intro\n## Blocked by\n- #12\n- #14 and #15\n\n#99 outside\n## Notes\n#100\n"
	got := FromChildBody(body)
	want := []int{12, 14, 15}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromChildBody() = %#v, want %#v", got, want)
	}
}

func TestFromParentRowReadsBlockedByTrailer(t *testing.T) {
	parentBody := "- [ ] #101 first child (blocked by #201, #202)\n- [x] #102 second child\n"
	got := FromParentRow(parentBody, 101)
	want := []int{201, 202}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromParentRow() = %#v, want %#v", got, want)
	}
	if rows := FromParentRow(parentBody, 102); len(rows) != 0 {
		t.Fatalf("FromParentRow(102) = %#v, want empty", rows)
	}
}

func TestDependenciesFormatsOpenAndClosedBlockers(t *testing.T) {
	children := []Child{
		{Num: 101, Body: "## Blocked by\n- #201\n- #202\n"},
	}
	states := map[int]string{201: "OPEN", 202: "CLOSED", 203: "OPEN"}
	parentBody := "- [ ] #101 parent dependency (blocked by #202, #203)\n"

	deps := Dependencies(children, true, parentBody, func(num int) string {
		return states[num]
	})

	got := FormatStatuses(deps[101])
	want := "OPEN #201, resolved #202, OPEN #203"
	if got != want {
		t.Fatalf("FormatStatuses() = %q, want %q", got, want)
	}
	if !HasOpen(deps[101]) {
		t.Fatal("HasOpen() = false, want true")
	}
}

func TestDependenciesSkipsParentRowsWhenExcluded(t *testing.T) {
	children := []Child{{Num: 101, Body: ""}}
	parentBody := "- [ ] #101 child (blocked by #201)\n"

	deps := Dependencies(children, false, parentBody, func(int) string { return "OPEN" })

	if got := deps[101]; len(got) != 0 {
		t.Fatalf("Dependencies() with includeParentRows=false = %#v, want empty", got)
	}
}

func TestDependenciesNormalizesStates(t *testing.T) {
	children := []Child{{Num: 1, Body: "## Blocked by\n- #2 #3\n"}}
	states := map[int]string{2: "open"} // #3 missing => "" => UNKNOWN

	deps := Dependencies(children, true, "", func(num int) string {
		return states[num]
	})

	want := []Status{{Num: 2, State: "OPEN"}, {Num: 3, State: "UNKNOWN"}}
	if !reflect.DeepEqual(deps[1], want) {
		t.Fatalf("Dependencies() = %#v, want %#v", deps[1], want)
	}
}

func TestWavesUseParentBlockerDepth(t *testing.T) {
	childNums := []int{1, 2, 3, 4}
	deps := map[int][]Status{
		1: nil,
		2: {{Num: 1, State: "CLOSED"}},
		3: {{Num: 2, State: "OPEN"}},
		4: {{Num: 99, State: "OPEN"}},
	}

	got := Waves(childNums, deps)

	want := map[int]int{1: 1, 2: 2, 3: 3, 4: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Waves() = %#v, want %#v", got, want)
	}
}

func TestWavesBreakBlockerCycles(t *testing.T) {
	childNums := []int{1, 2}
	deps := map[int][]Status{
		1: {{Num: 2, State: "OPEN"}},
		2: {{Num: 1, State: "OPEN"}},
	}

	got := Waves(childNums, deps)

	// The visiting guard treats the back-edge as wave 1, so the cycle
	// terminates with deterministic depths instead of recursing forever.
	want := map[int]int{1: 3, 2: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Waves() = %#v, want %#v", got, want)
	}
}

func TestFormatStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []Status
		want string
	}{
		{name: "empty", rows: nil, want: "-"},
		{name: "open", rows: []Status{{Num: 12, State: "OPEN"}}, want: "OPEN #12"},
		{name: "closed", rows: []Status{{Num: 14, State: "CLOSED"}}, want: "resolved #14"},
		{name: "unknown", rows: []Status{{Num: 15, State: "UNKNOWN"}}, want: "UNKNOWN #15"},
		{name: "blank state dashed", rows: []Status{{Num: 16, State: ""}}, want: "- #16"},
		{
			name: "mixed",
			rows: []Status{{Num: 12, State: "OPEN"}, {Num: 14, State: "CLOSED"}},
			want: "OPEN #12, resolved #14",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatStatuses(tc.rows); got != tc.want {
				t.Fatalf("FormatStatuses(%#v) = %q, want %q", tc.rows, got, tc.want)
			}
		})
	}
}

func TestHasOpen(t *testing.T) {
	if HasOpen(nil) {
		t.Fatal("HasOpen(nil) = true, want false")
	}
	if HasOpen([]Status{{Num: 1, State: "CLOSED"}, {Num: 2, State: "UNKNOWN"}}) {
		t.Fatal("HasOpen(no open rows) = true, want false")
	}
	if !HasOpen([]Status{{Num: 1, State: "CLOSED"}, {Num: 2, State: "OPEN"}}) {
		t.Fatal("HasOpen(open row) = false, want true")
	}
}
