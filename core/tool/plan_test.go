package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/plan"
)

func newTestPlanTool(t *testing.T) *Plan {
	t.Helper()
	dir := t.TempDir()
	return &Plan{Store: plan.NewStore(dir), SessionID: "test-sess"}
}

func TestPlanTool_Set(t *testing.T) {
	tool := newTestPlanTool(t)
	args := json.RawMessage(`{
        "operation":"set",
        "items":[
            {"text":"analyze repomap","status":"completed"},
            {"text":"refactor theme","status":"in_progress"},
            {"text":"add toggle","status":"pending"}
        ]
    }`)
	r, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Content)
	}
	cur, _ := tool.Store.Load(tool.SessionID)
	if len(cur.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(cur.Items))
	}
	if cur.Items[0].ID == "" {
		t.Errorf("set: items did not receive auto IDs")
	}
	if !strings.Contains(r.Content, "Plan set") {
		t.Errorf("missing op prefix: %q", r.Content)
	}
}

func TestPlanTool_AddItem(t *testing.T) {
	tool := newTestPlanTool(t)
	r, err := tool.Run(context.Background(), json.RawMessage(`{"operation":"add_item","text":"first"}`))
	if err != nil || r.IsError {
		t.Fatalf("add_item err=%v content=%s", err, r.Content)
	}
	r, _ = tool.Run(context.Background(), json.RawMessage(`{"operation":"add_item","text":"second","status":"in_progress"}`))
	if r.IsError {
		t.Fatalf("second add_item: %s", r.Content)
	}
	cur, _ := tool.Store.Load(tool.SessionID)
	if len(cur.Items) != 2 {
		t.Fatalf("got %d items", len(cur.Items))
	}
	if cur.Items[1].Status != plan.InProgress {
		t.Errorf("status not honoured: %s", cur.Items[1].Status)
	}
	if !strings.Contains(r.Content, "added") {
		t.Errorf("result missing 'added': %q", r.Content)
	}
}

func TestPlanTool_UpdateItem(t *testing.T) {
	tool := newTestPlanTool(t)
	_, _ = tool.Run(context.Background(), json.RawMessage(`{"operation":"add_item","text":"alpha"}`))
	cur, _ := tool.Store.Load(tool.SessionID)
	id := cur.Items[0].ID

	r, err := tool.Run(context.Background(), json.RawMessage(`{
        "operation":"update_item","id":"`+id+`","status":"completed"
    }`))
	if err != nil || r.IsError {
		t.Fatalf("update_item err=%v content=%s", err, r.Content)
	}
	cur, _ = tool.Store.Load(tool.SessionID)
	if cur.Items[0].Status != plan.Completed {
		t.Errorf("status not updated: %s", cur.Items[0].Status)
	}
}

func TestPlanTool_UpdateItem_MissingID(t *testing.T) {
	tool := newTestPlanTool(t)
	r, _ := tool.Run(context.Background(), json.RawMessage(`{"operation":"update_item","id":"nope","status":"completed"}`))
	if !r.IsError {
		t.Errorf("expected error for missing id, got: %s", r.Content)
	}
}

func TestPlanTool_RemoveItem(t *testing.T) {
	tool := newTestPlanTool(t)
	_, _ = tool.Run(context.Background(), json.RawMessage(`{
        "operation":"set",
        "items":[{"text":"a"},{"text":"b"},{"text":"c"}]
    }`))
	cur, _ := tool.Store.Load(tool.SessionID)
	id := cur.Items[1].ID

	r, _ := tool.Run(context.Background(), json.RawMessage(`{"operation":"remove_item","id":"`+id+`"}`))
	if r.IsError {
		t.Fatalf("remove_item: %s", r.Content)
	}
	cur, _ = tool.Store.Load(tool.SessionID)
	if len(cur.Items) != 2 {
		t.Errorf("got %d items, want 2", len(cur.Items))
	}
	for _, it := range cur.Items {
		if it.ID == id {
			t.Errorf("removed id still present: %s", id)
		}
	}
}

func TestPlanTool_List(t *testing.T) {
	tool := newTestPlanTool(t)
	_, _ = tool.Run(context.Background(), json.RawMessage(`{
        "operation":"set",
        "items":[{"text":"alpha","status":"completed"},{"text":"beta"}]
    }`))
	r, err := tool.Run(context.Background(), json.RawMessage(`{"operation":"list"}`))
	if err != nil || r.IsError {
		t.Fatalf("list: %s", r.Content)
	}
	if !strings.Contains(r.Content, "alpha") || !strings.Contains(r.Content, "beta") {
		t.Errorf("list missing items: %q", r.Content)
	}
	if !strings.Contains(r.Content, "Plan listed") {
		t.Errorf("list missing prefix: %q", r.Content)
	}
}

func TestPlanTool_UnknownOperation(t *testing.T) {
	tool := newTestPlanTool(t)
	r, _ := tool.Run(context.Background(), json.RawMessage(`{"operation":"reorder"}`))
	if !r.IsError {
		t.Errorf("expected error for unknown op")
	}
}

func TestPlanTool_Emit(t *testing.T) {
	tool := newTestPlanTool(t)
	var calls []string
	tool.Emit = func(ctx context.Context, p *plan.Plan, op string) {
		calls = append(calls, op)
	}
	_, _ = tool.Run(context.Background(), json.RawMessage(`{"operation":"add_item","text":"x"}`))
	cur, _ := tool.Store.Load(tool.SessionID)
	id := cur.Items[0].ID
	_, _ = tool.Run(context.Background(), json.RawMessage(`{"operation":"update_item","id":"`+id+`","status":"completed"}`))
	_, _ = tool.Run(context.Background(), json.RawMessage(`{"operation":"list"}`))

	if len(calls) != 2 {
		t.Fatalf("emit called %d times, want 2 (mutations only): %v", len(calls), calls)
	}
	if calls[0] != "add_item" || calls[1] != "update_item" {
		t.Errorf("emit ops = %v, want [add_item, update_item]", calls)
	}
}

func TestPlanTool_NotConfigured(t *testing.T) {
	tool := &Plan{} // no Store, no SessionID
	r, _ := tool.Run(context.Background(), json.RawMessage(`{"operation":"list"}`))
	if !r.IsError {
		t.Errorf("expected error when not configured")
	}
}
