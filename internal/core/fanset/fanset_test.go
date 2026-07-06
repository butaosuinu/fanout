package fanset

import (
	"reflect"
	"testing"
)

func intKey(n int) int { return n }

func TestSet(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want map[int]bool
	}{
		{name: "nil input yields empty non-nil set", in: nil, want: map[int]bool{}},
		{name: "dedupes repeated keys", in: []int{1, 1, 2}, want: map[int]bool{1: true, 2: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Set(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Set(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestUnion(t *testing.T) {
	tests := []struct {
		name string
		in   []map[int]bool
		want map[int]bool
	}{
		{name: "no inputs yields empty non-nil map", in: nil, want: map[int]bool{}},
		{
			name: "merges disjoint sets (migration fallback fold)",
			in:   []map[int]bool{{101: true}, {102: true}},
			want: map[int]bool{101: true, 102: true},
		},
		{
			name: "overlapping keys collapse",
			in:   []map[int]bool{{1: true, 2: true}, {2: true, 3: true}},
			want: map[int]bool{1: true, 2: true, 3: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Union(tt.in...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Union(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFilterOnlySkip(t *testing.T) {
	tests := []struct {
		name             string
		items            []int
		only             []int
		skip             []int
		wantKept         []int
		wantFilteredOnly []int
		wantFilteredSkip []int
		wantMissingOnly  []int
	}{
		{
			// Neither selector set: kept aliases the input and the three drop
			// slices stay nil, so the dry-run "filtered out:" section is skipped.
			name:     "no selectors returns input and nil drops",
			items:    []int{1, 2, 3},
			wantKept: []int{1, 2, 3},
		},
		{
			name:             "only keeps listed and reports missing",
			items:            []int{1, 2, 3},
			only:             []int{1, 3, 99},
			wantKept:         []int{1, 3},
			wantFilteredOnly: []int{2},
			wantMissingOnly:  []int{99},
		},
		{
			name:             "skip drops listed",
			items:            []int{1, 2, 3},
			skip:             []int{2},
			wantKept:         []int{1, 3},
			wantFilteredSkip: []int{2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, filteredOnly, filteredSkip, missingOnly := FilterOnlySkip(tt.items, intKey, tt.only, tt.skip)
			if !reflect.DeepEqual(kept, tt.wantKept) {
				t.Fatalf("kept = %#v, want %#v", kept, tt.wantKept)
			}
			if !reflect.DeepEqual(filteredOnly, tt.wantFilteredOnly) {
				t.Fatalf("filteredOnly = %#v, want %#v", filteredOnly, tt.wantFilteredOnly)
			}
			if !reflect.DeepEqual(filteredSkip, tt.wantFilteredSkip) {
				t.Fatalf("filteredSkip = %#v, want %#v", filteredSkip, tt.wantFilteredSkip)
			}
			if !reflect.DeepEqual(missingOnly, tt.wantMissingOnly) {
				t.Fatalf("missingOnly = %#v, want %#v", missingOnly, tt.wantMissingOnly)
			}
		})
	}
}

// TestFilterOnlySkipNoSelectorsAliasesInput pins that the no-selector path
// returns the exact input slice (kept is the same backing array), not a copy —
// the behavior both lanes relied on before this extraction.
func TestFilterOnlySkipNoSelectorsAliasesInput(t *testing.T) {
	items := []int{1, 2, 3}
	kept, _, _, _ := FilterOnlySkip(items, intKey, nil, nil)
	if len(kept) != len(items) || &kept[0] != &items[0] {
		t.Fatalf("kept did not alias the input slice")
	}
}

func TestSplitFanned(t *testing.T) {
	tests := []struct {
		name        string
		items       []int
		fanned      map[int]bool
		wantTargets []int
		wantSkipped []int
	}{
		{
			name:        "none fanned keeps all and skips nothing",
			items:       []int{1, 2},
			wantTargets: []int{1, 2},
		},
		{
			name:        "fanned keys collected in input order",
			items:       []int{1, 2, 3},
			fanned:      map[int]bool{1: true, 3: true},
			wantTargets: []int{2},
			wantSkipped: []int{1, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets, skipped := SplitFanned(tt.items, intKey, tt.fanned)
			if !reflect.DeepEqual(targets, tt.wantTargets) {
				t.Fatalf("targets = %#v, want %#v", targets, tt.wantTargets)
			}
			if !reflect.DeepEqual(skipped, tt.wantSkipped) {
				t.Fatalf("skipped = %#v, want %#v", skipped, tt.wantSkipped)
			}
		})
	}
}

func TestApplyLimit(t *testing.T) {
	tests := []struct {
		name         string
		items        []int
		limit        int
		wantTargets  []int
		wantDeferred []int
	}{
		{name: "zero limit defers nothing", items: []int{1, 2}, limit: 0, wantTargets: []int{1, 2}},
		{name: "limit at length defers nothing", items: []int{1, 2}, limit: 2, wantTargets: []int{1, 2}},
		{name: "limit below length splits", items: []int{1, 2, 3}, limit: 1, wantTargets: []int{1}, wantDeferred: []int{2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets, deferred := ApplyLimit(tt.items, tt.limit)
			if !reflect.DeepEqual(targets, tt.wantTargets) {
				t.Fatalf("targets = %#v, want %#v", targets, tt.wantTargets)
			}
			if !reflect.DeepEqual(deferred, tt.wantDeferred) {
				t.Fatalf("deferred = %#v, want %#v", deferred, tt.wantDeferred)
			}
		})
	}
}
