package team

import "testing"

func TestTaskPeerNumIsStableNegativeAndDistinct(t *testing.T) {
	const parent = "plan:launch-plan"
	a := TaskPeerNum(parent, "base-types")
	again := TaskPeerNum(parent, "base-types")
	if a != again {
		t.Fatalf("TaskPeerNum not deterministic: %d != %d", a, again)
	}
	if a >= 0 {
		t.Fatalf("TaskPeerNum(%q, base-types) = %d, want negative", parent, a)
	}
	b := TaskPeerNum(parent, "api-client")
	if a == b {
		t.Fatalf("distinct task ids collided: both %d", a)
	}
	// The parent ref is part of the key, so the same task id under a different
	// plan maps elsewhere.
	if TaskPeerNum("plan:other", "base-types") == a {
		t.Fatal("task id collided across plans")
	}
}

func TestIsPlanParent(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"plan:launch-plan", true},
		{"plan:x", true},
		{"68", false},
		{"@manual", false},
		{"https://github.com/users/u/projects/3", false},
		{"", false},
	} {
		if got := IsPlanParent(tc.ref); got != tc.want {
			t.Errorf("IsPlanParent(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}
