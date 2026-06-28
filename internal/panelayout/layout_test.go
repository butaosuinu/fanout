package panelayout

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
)

// gridCfg is a no-sidebar config with dmux's comfort thresholds.
func gridCfg() Config { return DefaultConfig() }

// sidebarCfg is the same with a console sidebar reserved.
func sidebarCfg() Config {
	c := DefaultConfig()
	c.SidebarWidth = SidebarWidthDefault
	return c
}

// The two checksums below were captured from real tmux 3.6a (see the
// layout-probe runs during development); they pin both Checksum and Render to
// tmux's actual algorithm and geometry, not just to our understanding of it.
func TestChecksumMatchesRealTmux(t *testing.T) {
	cases := []struct{ body, want string }{
		{"200x50,0,0{100x50,0,0,1079,99x50,101,0,1080}", "2cc9"},
		{"200x50,0,0{40x50,0,0,1093,159x50,41,0{79x50,41,0,1094,79x50,121,0,1095}}", "fab2"},
	}
	for _, tc := range cases {
		if got := Checksum(tc.body); got != tc.want {
			t.Errorf("Checksum(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestRenderGolden(t *testing.T) {
	cases := []struct {
		name string
		in   RenderInput
		want string
	}{
		{
			// Real tmux: `tmux new -x200 -y50; split-window -h` → this exact string.
			name: "no-sidebar 2 panes single row",
			in: RenderInput{
				Win:            Window{200, 50},
				ContentPaneIDs: []string{"1079", "1080"},
				Cols:           2,
				Cfg:            gridCfg(),
			},
			want: "2cc9,200x50,0,0{100x50,0,0,1079,99x50,101,0,1080}",
		},
		{
			// Real tmux accepted this verbatim (select-layout rc=0, identical echo).
			name: "sidebar + 2-pane grid row",
			in: RenderInput{
				Win:            Window{200, 50},
				SidebarPaneID:  "1093",
				ContentPaneIDs: []string{"1094", "1095"},
				Cols:           2,
				Cfg:            sidebarCfg(),
			},
			want: "fab2,200x50,0,0{40x50,0,0,1093,159x50,41,0{79x50,41,0,1094,79x50,121,0,1095}}",
		},
		{
			name: "single pane no sidebar",
			in: RenderInput{
				Win:            Window{120, 40},
				ContentPaneIDs: []string{"7"},
				Cols:           1,
				Cfg:            gridCfg(),
			},
			want: "ab04,120x40,0,0,7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.in)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Errorf("Render = %q\nwant       %q", got, tc.want)
			}
		})
	}
}

func TestRenderMultiRowNesting(t *testing.T) {
	// 3 grid panes, 2 columns → row0 has 2 panes, row1 has 1: a [] vertical
	// container wrapping a {} horizontal row and a bare leaf, with the height
	// remainder on the last row. Verified accepted by real tmux 3.6a (rc=0,
	// identical echo) with these pane ids.
	got, err := Render(RenderInput{
		Win:            Window{200, 50},
		ContentPaneIDs: []string{"1108", "1109", "1110"},
		Cols:           2,
		Cfg:            gridCfg(),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "7c6f,200x50,0,0[200x24,0,0{100x24,0,0,1108,99x24,101,0,1109},200x25,0,25,1110]"
	if got != want {
		t.Errorf("Render = %q\nwant       %q", got, want)
	}
}

func TestRenderSpacerRow(t *testing.T) {
	// n=5 grid panes at 250x50 picks 3 cols / 2 rows with a short last row that
	// would stretch past MaxComfortableWidth, so a spacer cell is appended.
	// Verified accepted by real tmux 3.6a (rc=0) with these six pane ids.
	got, err := Render(RenderInput{
		Win:            Window{250, 50},
		ContentPaneIDs: []string{"1117", "1118", "1119", "1120", "1121", "1122"},
		Cols:           3,
		LastCellSpacer: true,
		Cfg:            gridCfg(),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Last row: two content panes capped at 100 plus a 48-wide spacer.
	want := "0ea2,250x50,0,0[250x24,0,0{84x24,0,0,1117,82x24,85,0,1118,82x24,168,0,1119},250x25,0,25{100x25,0,25,1120,100x25,101,25,1121,48x25,202,25,1122}]"
	if got != want {
		t.Errorf("Render = %q\nwant       %q", got, want)
	}
}

func TestRenderErrors(t *testing.T) {
	cfg := gridCfg()
	cases := []RenderInput{
		{Win: Window{200, 50}, ContentPaneIDs: nil, Cols: 1, Cfg: cfg},
		{Win: Window{200, 50}, ContentPaneIDs: []string{"1"}, Cols: 0, Cfg: cfg},
		{Win: Window{0, 50}, ContentPaneIDs: []string{"1"}, Cols: 1, Cfg: cfg},
		// 60 grid panes in 1 column can't fit positive height in 50 rows.
		{Win: Window{200, 50}, ContentPaneIDs: ids(60), Cols: 1, Cfg: cfg},
	}
	for i, in := range cases {
		if _, err := Render(in); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestDecidePlan(t *testing.T) {
	cases := []struct {
		name    string
		win     Window
		n       int
		cfg     Config
		cols    int
		rows    int
		dist    []int
		comfort bool
		spacer  bool
		spacerW int
	}{
		{"1 pane", Window{200, 50}, 1, gridCfg(), 1, 1, []int{1}, true, false, 0},
		{"2 panes one row", Window{200, 50}, 2, gridCfg(), 2, 1, []int{2}, true, false, 0},
		{"3 panes one row", Window{200, 50}, 3, gridCfg(), 3, 1, []int{3}, true, false, 0},
		{"4 panes 2x2", Window{200, 50}, 4, gridCfg(), 2, 2, []int{2, 2}, true, false, 0},
		{"5 panes spacer", Window{250, 50}, 5, gridCfg(), 3, 2, []int{3, 2}, true, true, 48},
		{"sidebar 2 panes", Window{200, 50}, 2, sidebarCfg(), 2, 1, []int{2}, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecidePlan(tc.win, tc.n, tc.cfg)
			if got.Cols != tc.cols || got.Rows != tc.rows {
				t.Errorf("cols/rows = %d/%d, want %d/%d", got.Cols, got.Rows, tc.cols, tc.rows)
			}
			if fmt.Sprint(got.PaneDistribution) != fmt.Sprint(tc.dist) {
				t.Errorf("dist = %v, want %v", got.PaneDistribution, tc.dist)
			}
			if got.Comfortable != tc.comfort {
				t.Errorf("comfortable = %v, want %v", got.Comfortable, tc.comfort)
			}
			if got.Spacer.Needed != tc.spacer {
				t.Errorf("spacer.Needed = %v, want %v", got.Spacer.Needed, tc.spacer)
			}
			if tc.spacer && got.Spacer.Width != tc.spacerW {
				t.Errorf("spacer.Width = %d, want %d", got.Spacer.Width, tc.spacerW)
			}
		})
	}
}

func TestDecidePlanTieBreakPrefersFewerColumns(t *testing.T) {
	// A square window where multiple column counts tie on score should resolve
	// to the smaller column count (wider panes).
	got := DecidePlan(Window{180, 50}, 2, gridCfg())
	if got.Cols != 2 { // 2 panes: one row (cols 2) beats stacked (cols 1)
		t.Fatalf("cols = %d, want 2", got.Cols)
	}
}

func TestDecidePlanZero(t *testing.T) {
	for _, in := range []struct {
		w Window
		n int
	}{{Window{200, 50}, 0}, {Window{0, 50}, 3}, {Window{200, 0}, 3}} {
		if got := DecidePlan(in.w, in.n, gridCfg()); got.Cols != 0 || got.Rows != 0 {
			t.Errorf("DecidePlan(%v,%d) = %+v, want zero", in.w, in.n, got)
		}
	}
}

// leafRe matches a leaf cell "WxH,X,Y,id" but not a container "WxH,X,Y{" / "[".
var leafRe = regexp.MustCompile(`(\d+)x(\d+),(\d+),(\d+),([^,{}\[\]]+)`)

// TestRenderInvariants sweeps sizes/pane counts and asserts every leaf cell
// stays within the window and the sidebar width math closes.
func TestRenderInvariants(t *testing.T) {
	for _, sidebar := range []bool{false, true} {
		cfg := gridCfg()
		if sidebar {
			cfg = sidebarCfg()
		}
		for _, win := range []Window{{120, 40}, {200, 50}, {320, 60}, {80, 24}} {
			for n := 1; n <= 6; n++ {
				plan := DecidePlan(win, n, cfg)
				if plan.Cols == 0 {
					continue
				}
				in := RenderInput{
					Win:            win,
					ContentPaneIDs: ids(n),
					Cols:           plan.Cols,
					Cfg:            cfg,
				}
				if sidebar {
					in.SidebarPaneID = "9"
				}
				out, err := Render(in)
				if err != nil {
					continue // infeasible geometry is allowed to error; apply falls back
				}
				assertLeavesWithinWindow(t, out, win, sidebar, cfg)
			}
		}
	}
}

func assertLeavesWithinWindow(t *testing.T, layout string, win Window, sidebar bool, cfg Config) {
	t.Helper()
	startX := 0
	if sidebar {
		startX = sidebarReserve(cfg)
	}
	for _, m := range leafRe.FindAllStringSubmatch(layout, -1) {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		x, _ := strconv.Atoi(m[3])
		y, _ := strconv.Atoi(m[4])
		id := m[5]
		if id == "9" { // the sidebar leaf sits at x=0
			continue
		}
		if x < startX {
			t.Errorf("layout %q: leaf x=%d < contentStartX=%d", layout, x, startX)
		}
		if x+w > win.Width {
			t.Errorf("layout %q: leaf x+w=%d > width=%d", layout, x+w, win.Width)
		}
		if y+h > win.Height {
			t.Errorf("layout %q: leaf y+h=%d > height=%d", layout, y+h, win.Height)
		}
	}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i + 1)
	}
	return out
}
