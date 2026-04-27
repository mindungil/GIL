package app

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPlanPane_Empty(t *testing.T) {
	out := renderPlanPane(40, PlanSnapshot{NotFound: true})
	if !strings.Contains(out, "no plan yet") {
		t.Errorf("empty plan should hint user; got %q", out)
	}
}

func TestRenderPlanPane_OneItem(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	snap := PlanSnapshot{
		Items: []PlanItemView{
			{ID: "i1", Text: "analyze repomap", Status: "in_progress"},
		},
		UpdatedAt: time.Now(),
		Version:   1,
	}
	out := renderPlanPane(40, snap)
	if !strings.Contains(out, "analyze repomap") {
		t.Errorf("missing text: %q", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("missing in_progress glyph: %q", out)
	}
}

func TestRenderPlanPane_ThreeItemsAllStatuses(t *testing.T) {
	// Force Unicode mode for the duration of this test so the glyph
	// assertions don't depend on the runner's locale (CI commonly has
	// LANG=C which the package-level detector picks up).
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	snap := PlanSnapshot{
		Items: []PlanItemView{
			{ID: "i1", Text: "analyze repomap", Status: "completed"},
			{ID: "i2", Text: "refactor theme provider", Status: "in_progress"},
			{ID: "i3", Text: "add toggle", Status: "pending"},
		},
		Version: 7,
	}
	out := renderPlanPane(60, snap)
	for _, want := range []string{"analyze repomap", "refactor theme", "add toggle"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("missing ✓ in:\n%s", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("missing ● in:\n%s", out)
	}
	if !strings.Contains(out, "○") {
		t.Errorf("missing ○ in:\n%s", out)
	}
}

func TestPlanCounts(t *testing.T) {
	items := []PlanItemView{
		{Status: "completed"},
		{Status: "in_progress", Sub: []PlanItemView{
			{Status: "completed"},
			{Status: "pending"},
		}},
		{Status: "pending"},
	}
	pen, ip, comp := planCounts(items)
	if pen != 2 || ip != 1 || comp != 2 {
		t.Errorf("counts = (%d,%d,%d), want (2,1,2)", pen, ip, comp)
	}
}

func TestPlanPaneTitle(t *testing.T) {
	cases := []struct {
		name string
		snap PlanSnapshot
		want string
	}{
		{
			name: "empty",
			snap: PlanSnapshot{NotFound: true},
			want: "Plan (none)",
		},
		{
			name: "one item, no time",
			snap: PlanSnapshot{Items: []PlanItemView{{Text: "x"}}},
			want: "Plan (1 item)",
		},
		{
			name: "three items, no time",
			snap: PlanSnapshot{Items: []PlanItemView{{}, {}, {}}},
			want: "Plan (3 items)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planPaneTitle(c.snap)
			if got != c.want {
				t.Errorf("planPaneTitle = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPlanPaneTitle_WithRelativeTime(t *testing.T) {
	snap := PlanSnapshot{
		Items:     []PlanItemView{{Text: "a"}},
		UpdatedAt: time.Now().Add(-2 * time.Minute),
	}
	got := planPaneTitle(snap)
	if !strings.Contains(got, "Plan (1 item, ") {
		t.Errorf("got %q, want prefix 'Plan (1 item, '", got)
	}
	if !strings.Contains(got, "ago)") {
		t.Errorf("got %q, want 'ago)' suffix", got)
	}
}

func TestRenderPlanPane_SubItems(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	snap := PlanSnapshot{
		Items: []PlanItemView{
			{ID: "i1", Text: "top", Status: "in_progress", Sub: []PlanItemView{
				{ID: "i1.1", Text: "child A", Status: "completed"},
				{ID: "i1.2", Text: "child B", Status: "pending"},
			}},
		},
	}
	out := renderPlanPane(60, snap)
	for _, want := range []string{"top", "child A", "child B"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
