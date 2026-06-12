package sessionview

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
)

var errGraphBoom = errors.New("boom")

// Compile-time check: the gh-backed runner the TUI passes in satisfies the
// graph client interface.
var _ IssueGraphClient = ghissue.Runner{}

// fakeGraphClient is an in-memory IssueGraphClient. Per-issue errors are
// injected via detailErr; stateCalls counts IssueState lookups so tests can
// assert the per-call cache.
type fakeGraphClient struct {
	parentBody    string
	parentBodyErr error
	subIssues     []ghissue.Issue
	subIssuesErr  error
	details       map[int]ghissue.Issue
	detailErr     map[int]error
	detailCalls   map[int]int
	states        map[int]string
	stateCalls    map[int]int
}

func (f *fakeGraphClient) ParentBody(int) (string, error) {
	if f.parentBodyErr != nil {
		return "", f.parentBodyErr
	}
	return f.parentBody, nil
}

func (f *fakeGraphClient) SubIssueList(int) ([]ghissue.Issue, error) {
	if f.subIssuesErr != nil {
		return nil, f.subIssuesErr
	}
	return f.subIssues, nil
}

func (f *fakeGraphClient) IssueDetail(num int) (ghissue.Issue, error) {
	if f.detailCalls == nil {
		f.detailCalls = map[int]int{}
	}
	f.detailCalls[num]++
	if err := f.detailErr[num]; err != nil {
		return ghissue.Issue{}, err
	}
	detail, ok := f.details[num]
	if !ok {
		return ghissue.Issue{}, errGraphBoom
	}
	return detail, nil
}

func (f *fakeGraphClient) IssueState(num int) (string, error) {
	if f.stateCalls == nil {
		f.stateCalls = map[int]int{}
	}
	f.stateCalls[num]++
	state, ok := f.states[num]
	if !ok {
		return "", errGraphBoom // real gh returns no state on failure
	}
	return state, nil
}

func childNumbers(graph WaveGraph) []int {
	nums := make([]int, 0, len(graph.Children))
	for _, issue := range graph.Children {
		nums = append(nums, issue.Number)
	}
	return nums
}

func TestFetchWaveGraphNumericParentUnionsSources(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "**wave1**\n- [ ] #101 first\n**wave2**\n- [ ] #102 second (blocked by #201)\n",
		subIssues:  []ghissue.Issue{{Number: 101, Title: "first", State: "OPEN"}},
		details: map[int]ghissue.Issue{
			// Hydration target: sub-issue rows have no body.
			101: {Number: 101, Title: "first", State: "OPEN", Body: "## Blocked by\n- #102\n"},
			102: {Number: 102, Title: "second", State: "OPEN", Body: "x"},
			103: {Number: 103, Title: "recorded", State: "OPEN", Body: "x"},
		},
		states: map[int]string{102: "OPEN", 201: "CLOSED"},
	}

	graph, err := FetchWaveGraph(client, "100", []int{103, 103, 0})
	if err != nil {
		t.Fatalf("FetchWaveGraph() error = %v", err)
	}

	if got, want := childNumbers(graph), []int{101, 102, 103}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
	wantInfo := map[int]WaveInfo{
		101: {Wave: 3, WaveLabel: "wave1", Blockers: []blockers.Status{{Num: 102, State: "OPEN"}}, Blocked: true},
		102: {Wave: 2, WaveLabel: "wave2", Blockers: []blockers.Status{{Num: 201, State: "CLOSED"}}, Blocked: false},
		103: {Wave: 1, WaveLabel: "", Blockers: []blockers.Status{}, Blocked: false},
	}
	if !reflect.DeepEqual(graph.Info, wantInfo) {
		t.Fatalf("Info = %#v, want %#v", graph.Info, wantInfo)
	}
}

func TestFetchWaveGraphSubIssueFailureKeepsTaskListChildren(t *testing.T) {
	client := &fakeGraphClient{
		parentBody:   "- [ ] #101 first\n- [ ] #102 second\n",
		subIssuesErr: errGraphBoom,
		details: map[int]ghissue.Issue{
			101: {Number: 101, Title: "first", State: "OPEN", Body: "x"},
			102: {Number: 102, Title: "second", State: "OPEN", Body: "x"},
		},
	}

	graph, err := FetchWaveGraph(client, "100", nil)

	if err == nil || !strings.Contains(err.Error(), "sub-issues #100") {
		t.Fatalf("FetchWaveGraph() error = %v, want sub-issues #100 partial error", err)
	}
	if got, want := childNumbers(graph), []int{101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
}

func TestFetchWaveGraphRecordedLookupFailureKeepsOthers(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "- [ ] #101 first\n",
		details: map[int]ghissue.Issue{
			101: {Number: 101, Title: "first", State: "OPEN", Body: "x"},
		},
		detailErr: map[int]error{103: errGraphBoom},
	}

	graph, err := FetchWaveGraph(client, "100", []int{103})

	if err == nil || !strings.Contains(err.Error(), "#103") {
		t.Fatalf("FetchWaveGraph() error = %v, want #103 partial error", err)
	}
	if got, want := childNumbers(graph), []int{101}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
}

func TestFetchWaveGraphNonNumericParentUsesRecordedOnly(t *testing.T) {
	client := &fakeGraphClient{
		// Would join an error if the parent-issue path were consulted.
		parentBodyErr: errGraphBoom,
		subIssuesErr:  errGraphBoom,
		details: map[int]ghissue.Issue{
			5: {Number: 5, Title: "five", State: "OPEN", Body: "## Blocked by\n- #6\n"},
			6: {Number: 6, Title: "six", State: "OPEN", Body: "x"},
		},
		states: map[int]string{6: "OPEN"},
	}

	graph, err := FetchWaveGraph(client, "@manual", []int{5, 5, 0, 6})
	if err != nil {
		t.Fatalf("FetchWaveGraph() error = %v", err)
	}

	if got, want := childNumbers(graph), []int{5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
	if got := graph.Info[5]; got.Wave != 2 || !got.Blocked {
		t.Fatalf("Info[5] = %#v, want wave 2 blocked by open #6", got)
	}
	if got := graph.Info[6]; got.Wave != 1 || got.Blocked {
		t.Fatalf("Info[6] = %#v, want unblocked wave 1", got)
	}
}

func TestFetchWaveGraphHydrationErrorTolerant(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "",
		subIssues: []ghissue.Issue{
			{Number: 101, Title: "first", State: "OPEN"},
			{Number: 102, Title: "second", State: "OPEN"},
		},
		details: map[int]ghissue.Issue{
			102: {Number: 102, Title: "second", State: "OPEN", Body: "## Blocked by\n- #101\n"},
		},
		detailErr: map[int]error{101: errGraphBoom},
		states:    map[int]string{101: "OPEN"},
	}

	graph, err := FetchWaveGraph(client, "100", nil)

	if err == nil || !strings.Contains(err.Error(), "#101") {
		t.Fatalf("FetchWaveGraph() error = %v, want #101 hydration error", err)
	}
	if got, want := childNumbers(graph), []int{101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
	// The degraded row still gets wave/blocker info from what loaded.
	if got := graph.Info[102]; got.Wave != 2 || !got.Blocked {
		t.Fatalf("Info[102] = %#v, want wave 2 blocked by #101", got)
	}
	if got := graph.Info[101]; got.Wave != 1 {
		t.Fatalf("Info[101] = %#v, want wave 1", got)
	}
}

func TestFetchWaveGraphPreSeedsInSetBlockerStates(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "- [ ] #101 first (blocked by #102)\n- [ ] #102 second\n",
		details: map[int]ghissue.Issue{
			101: {Number: 101, Title: "first", State: "OPEN", Body: "x"},
			102: {Number: 102, Title: "second", State: "CLOSED", Body: "x"},
		},
		// states is empty: an IssueState call for #102 would error, so the
		// assertions below also prove the pre-seeded cache short-circuits it.
	}

	graph, err := FetchWaveGraph(client, "100", nil)
	if err != nil {
		t.Fatalf("FetchWaveGraph() error = %v", err)
	}

	if len(client.stateCalls) != 0 {
		t.Fatalf("IssueState calls = %v, want none (in-set states pre-seeded)", client.stateCalls)
	}
	want := []blockers.Status{{Num: 102, State: "CLOSED"}}
	if got := graph.Info[101].Blockers; !reflect.DeepEqual(got, want) {
		t.Fatalf("Info[101].Blockers = %#v, want %#v", got, want)
	}
	if graph.Info[101].Blocked {
		t.Fatal("Info[101].Blocked = true, want false (blocker is CLOSED)")
	}
}

func TestFetchWaveGraphIssueStateFailureSurfacesInError(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "- [ ] #101 first (blocked by #300)\n",
		details: map[int]ghissue.Issue{
			101: {Number: 101, Title: "first", State: "OPEN", Body: "x"},
		},
		// #300 is out-of-set and missing from states, so IssueState fails.
	}

	graph, err := FetchWaveGraph(client, "100", nil)

	if err == nil || !strings.Contains(err.Error(), "blocker state #300") {
		t.Fatalf("FetchWaveGraph() error = %v, want blocker state #300 partial error", err)
	}
	if got, want := childNumbers(graph), []int{101}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %#v, want %#v", got, want)
	}
	// Partial results survive: the failed lookup renders UNKNOWN downstream.
	want := []blockers.Status{{Num: 300, State: "UNKNOWN"}}
	if got := graph.Info[101].Blockers; !reflect.DeepEqual(got, want) {
		t.Fatalf("Info[101].Blockers = %#v, want %#v", got, want)
	}
}

func TestFetchWaveGraphCachesExternalBlockerStates(t *testing.T) {
	client := &fakeGraphClient{
		parentBody: "- [ ] #101 first (blocked by #300)\n- [ ] #102 second (blocked by #300)\n",
		details: map[int]ghissue.Issue{
			101: {Number: 101, Title: "first", State: "OPEN", Body: "x"},
			102: {Number: 102, Title: "second", State: "OPEN", Body: "x"},
		},
		states: map[int]string{300: "OPEN"},
	}

	graph, err := FetchWaveGraph(client, "100", nil)
	if err != nil {
		t.Fatalf("FetchWaveGraph() error = %v", err)
	}

	if got := client.stateCalls[300]; got != 1 {
		t.Fatalf("IssueState(#300) calls = %d, want 1 (cached)", got)
	}
	for _, num := range []int{101, 102} {
		want := []blockers.Status{{Num: 300, State: "OPEN"}}
		if got := graph.Info[num].Blockers; !reflect.DeepEqual(got, want) {
			t.Fatalf("Info[%d].Blockers = %#v, want %#v", num, got, want)
		}
	}
}

func TestFetchWaveGraphSkipsRehydratingDetailLoadedChildren(t *testing.T) {
	t.Parallel()

	// 201 comes from a recorded pane via IssueDetail with a genuinely empty
	// body; hydration must not fetch it a second time.
	client := &fakeGraphClient{
		parentBody: "",
		subIssues:  []ghissue.Issue{{Number: 101, Title: "sub", State: "OPEN", Body: "x"}},
		details: map[int]ghissue.Issue{
			201: {Number: 201, Title: "recorded", State: "OPEN", Body: ""},
		},
	}

	graph, err := FetchWaveGraph(client, "100", []int{201})
	if err != nil {
		t.Fatalf("FetchWaveGraph() error = %v", err)
	}
	if got := client.detailCalls[201]; got != 1 {
		t.Fatalf("IssueDetail(#201) calls = %d, want 1 (no re-hydration)", got)
	}
	if _, ok := graph.Info[201]; !ok {
		t.Fatalf("Info missing #201: %#v", graph.Info)
	}
}
