package selfupdate

import "testing"

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
