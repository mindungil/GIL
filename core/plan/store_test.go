package plan

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestLoad_MissingReturnsEmpty confirms a brand-new session gets an
// empty Plan (not an error). The caller path is the AgentLoop reading
// the plan before the agent has ever called the tool.
func TestLoad_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p, err := s.Load("sess1")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if p == nil {
		t.Fatal("Load returned nil plan")
	}
	if !p.IsEmpty() {
		t.Errorf("expected empty plan, got %d items", len(p.Items))
	}
	if p.SessionID != "sess1" {
		t.Errorf("session id not stamped: %q", p.SessionID)
	}
}

// TestSaveLoad_RoundTrip confirms a saved plan reads back identical.
func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	in := &Plan{Items: []Item{
		{Text: "analyze repomap", Status: Completed},
		{Text: "refactor theme", Status: InProgress, Note: "hard part"},
		{Text: "add toggle", Status: Pending},
	}}
	if err := s.Save("s1", in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load("s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Items) != 3 {
		t.Fatalf("got %d items", len(out.Items))
	}
	for i, it := range out.Items {
		if it.ID == "" {
			t.Errorf("item %d missing ID", i)
		}
		if it.Text != in.Items[i].Text {
			t.Errorf("item %d text mismatch", i)
		}
	}
}

// TestSave_VersionIncrements tracks Version/UpdatedAt bumps so the TUI
// observers can dedup stale plans.
func TestSave_VersionIncrements(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	p := &Plan{Items: []Item{{Text: "x"}}}
	if err := s.Save("a", p); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if p.Version != 1 {
		t.Errorf("version after first save = %d, want 1", p.Version)
	}
	first := p.UpdatedAt

	p.Items = append(p.Items, Item{Text: "y"})
	if err := s.Save("a", p); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	if p.Version != 2 {
		t.Errorf("version after second save = %d, want 2", p.Version)
	}
	if !p.UpdatedAt.After(first) && !p.UpdatedAt.Equal(first) {
		t.Errorf("UpdatedAt regressed")
	}
}

// TestSave_AssignsIDs verifies the auto-generated "iN" scheme.
func TestSave_AssignsIDs(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{
		{Text: "first"},
		{Text: "second", Sub: []Item{{Text: "child"}}},
		{Text: "third"},
	}}
	if err := s.Save("k", p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := p.Items[0].ID; got != "i1" {
		t.Errorf("items[0].ID = %q, want i1", got)
	}
	if got := p.Items[1].ID; got != "i2" {
		t.Errorf("items[1].ID = %q, want i2", got)
	}
	if got := p.Items[1].Sub[0].ID; got != "i2.1" {
		t.Errorf("items[1].Sub[0].ID = %q, want i2.1", got)
	}
	if got := p.Items[2].ID; got != "i3" {
		t.Errorf("items[2].ID = %q, want i3", got)
	}
}

// TestSave_PreservesProvidedIDs makes sure a partially-id'd input
// doesn't clobber the agent's chosen IDs and doesn't collide on
// auto-assignment.
func TestSave_PreservesProvidedIDs(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{
		{ID: "i5", Text: "explicit"},
		{Text: "auto"},
	}}
	if err := s.Save("k", p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if p.Items[0].ID != "i5" {
		t.Errorf("explicit ID lost: %q", p.Items[0].ID)
	}
	// Auto-assignment should not collide with i5; counter should jump.
	if p.Items[1].ID == "i5" {
		t.Errorf("auto ID collided with provided i5")
	}
}

// TestSave_RejectsInvalidStatus refuses statuses outside the 3-state set.
func TestSave_RejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{{Text: "bad", Status: "halfway"}}}
	err := s.Save("k", p)
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expected ErrInvalidStatus, got %v", err)
	}
}

// TestSave_RejectsTwoLevelDeepSub flattens or errors when a sub has its
// own sub. Per spec we error so the agent sees the failure and can flatten.
func TestSave_RejectsTwoLevelDeepSub(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{{Text: "top", Sub: []Item{
		{Text: "child", Sub: []Item{{Text: "grandchild"}}},
	}}}}
	err := s.Save("k", p)
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("expected ErrTooDeep, got %v", err)
	}
}

// TestSave_FileMode confirms 0644 (plan content is not secret).
func TestSave_FileMode(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{{Text: "x"}}}
	if err := s.Save("m", p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "m", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != fs.FileMode(0o644) {
		t.Errorf("file mode = %v, want 0644", got)
	}
}

// TestSave_AtomicTmpfileCleanup checks no leftover .tmp files survive a
// successful save.
func TestSave_AtomicTmpfileCleanup(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{{Text: "x"}}}
	if err := s.Save("c", p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "c"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".tmp" || (len(name) > 4 && name[len(name)-4:] == ".tmp") {
			t.Errorf("leftover tmpfile %q", name)
		}
		if len(name) >= 5 && name[:5] == "plan." && name != "plan.json" {
			// catches "plan.json.tmp.*"
			t.Errorf("leftover save tmp %q", name)
		}
	}
}

// TestSave_ConcurrentDoesNotCorrupt fires N concurrent saves with
// distinct content and asserts the on-disk file is still valid JSON
// after the storm. The exact winner doesn't matter — corruption-free
// is the contract.
func TestSave_ConcurrentDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	const N = 30
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := &Plan{Items: []Item{
				{Text: "a"},
				{Text: "b"},
			}}
			_ = s.Save("z", p)
		}(i)
	}
	wg.Wait()
	body, err := os.ReadFile(filepath.Join(dir, "z", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Plan
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("post-storm JSON corrupt: %v", err)
	}
	if len(p.Items) != 2 {
		t.Errorf("expected 2 items after storm, got %d", len(p.Items))
	}
}

// TestClear_RemovesFile and silently succeeds on missing.
func TestClear_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	p := &Plan{Items: []Item{{Text: "x"}}}
	if err := s.Save("d", p); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear("d"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "plan.json")); !os.IsNotExist(err) {
		t.Errorf("file still present after Clear: err=%v", err)
	}
	// Idempotent.
	if err := s.Clear("d"); err != nil {
		t.Errorf("second Clear errored: %v", err)
	}
}

// TestCounts_PendingInProgressCompleted ensures sub-items also count.
func TestCounts_PendingInProgressCompleted(t *testing.T) {
	p := &Plan{Items: []Item{
		{Status: Completed},
		{Status: InProgress, Sub: []Item{
			{Status: Completed},
			{Status: Pending},
		}},
		{Status: Pending},
	}}
	pen, ip, comp := p.Counts()
	if pen != 2 || ip != 1 || comp != 2 {
		t.Errorf("counts = (%d,%d,%d), want (2,1,2)", pen, ip, comp)
	}
}
