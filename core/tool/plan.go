package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mindungil/gil/core/plan"
)

// PlanEmitter is the optional callback the runner wires so plan
// mutations emit a `plan_updated` event over the per-session stream.
// nil-safe: when unset the tool simply skips the emission. Defined as a
// function field rather than an interface so the runner can pass a
// closure that already has a reference to its event.Stream.
type PlanEmitter func(ctx context.Context, p *plan.Plan, op string)

// Plan is the agent-callable tool that manages the per-session plan.
//
// The shape is borrowed from opencode (todo.ts: agent passes the
// complete updated list each call) AND codex (plan.rs: id-stable items
// with optional sub-items). We support both shapes via the `operation`
// discriminator: callers can either rewrite the entire plan with a
// `set` op (the common path) or surgically edit one item with
// `update_item` / `add_item` / `remove_item` / `list`.
//
// SessionID must be non-empty when constructing the tool. Store may be
// nil for tests; in that case Run reports the missing wiring rather
// than panicking, so the loop's "tool error" path keeps the model
// alive instead of crashing the run.
type Plan struct {
	Store     *plan.Store
	SessionID string
	Emit      PlanEmitter
}

const planSchema = `{
  "type":"object",
  "properties":{
    "operation":{
      "type":"string",
      "description":"set|update_item|add_item|remove_item|list",
      "enum":["set","update_item","add_item","remove_item","list"]
    },
    "items":{
      "type":"array",
      "description":"For 'set': the complete list of plan items, replaces the existing plan.",
      "items":{
        "type":"object",
        "properties":{
          "id":{"type":"string"},
          "text":{"type":"string"},
          "status":{"type":"string","enum":["pending","in_progress","completed"]},
          "note":{"type":"string"},
          "sub":{
            "type":"array",
            "items":{
              "type":"object",
              "properties":{
                "id":{"type":"string"},
                "text":{"type":"string"},
                "status":{"type":"string","enum":["pending","in_progress","completed"]},
                "note":{"type":"string"}
              },
              "required":["text"]
            }
          }
        },
        "required":["text"]
      }
    },
    "id":{"type":"string","description":"Item ID for update_item / remove_item."},
    "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"New status for update_item."},
    "text":{"type":"string","description":"New text for update_item / add_item."},
    "note":{"type":"string","description":"Optional note for update_item / add_item."}
  },
  "required":["operation"]
}`

// Name implements tool.Tool.
func (p *Plan) Name() string { return "plan" }

// Description implements tool.Tool. Phrasing nudges the agent to
// rewrite the plan when it changes meaningfully (rather than emitting
// dozens of tiny update_item calls) — matches opencode/cline guidance.
func (p *Plan) Description() string {
	return "Manage the run's plan (TODO checklist). The plan persists across compactions and tool calls. The user sees this plan in the TUI/CLI. Operations: 'set' (replace the whole list), 'update_item' (status/text by id), 'add_item' (append), 'remove_item' (by id), 'list' (read-only). Use 'set' to refine the plan; use 'update_item' to mark progress as you go."
}

// Schema implements tool.Tool.
func (p *Plan) Schema() json.RawMessage { return json.RawMessage(planSchema) }

// Run dispatches on the operation. All mutations re-load the plan
// before applying the delta so concurrent tool calls (parallel sub-
// agents) don't lose each other's updates — at the cost of a
// last-writer-wins for set when two agents race the same op.
func (p *Plan) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	if p.Store == nil || p.SessionID == "" {
		return Result{Content: "plan tool not configured for this run", IsError: true}, nil
	}
	var args struct {
		Operation string      `json:"operation"`
		Items     []plan.Item `json:"items"`
		ID        string      `json:"id"`
		Status    plan.Status `json:"status"`
		Text      string      `json:"text"`
		Note      string      `json:"note"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return Result{}, fmt.Errorf("plan unmarshal: %w", err)
	}
	switch args.Operation {
	case "list":
		cur, err := p.Store.Load(p.SessionID)
		if err != nil {
			return Result{Content: "plan list failed: " + err.Error(), IsError: true}, nil
		}
		return Result{Content: renderPlanResult("listed", cur)}, nil

	case "set":
		// Replace the whole plan with the agent's items.
		newPlan := &plan.Plan{Items: args.Items}
		if err := p.Store.Save(p.SessionID, newPlan); err != nil {
			return Result{Content: "plan set failed: " + err.Error(), IsError: true}, nil
		}
		p.fireEmit(ctx, newPlan, "set")
		return Result{Content: renderPlanResult("set", newPlan)}, nil

	case "update_item":
		if args.ID == "" {
			return Result{Content: "update_item requires id", IsError: true}, nil
		}
		cur, err := p.Store.Load(p.SessionID)
		if err != nil {
			return Result{Content: "plan load failed: " + err.Error(), IsError: true}, nil
		}
		if !mutateItem(cur, args.ID, func(it *plan.Item) {
			if args.Status != "" {
				it.Status = args.Status
			}
			if args.Text != "" {
				it.Text = args.Text
			}
			if args.Note != "" {
				it.Note = args.Note
			}
		}) {
			return Result{Content: fmt.Sprintf("plan update_item: id %q not found", args.ID), IsError: true}, nil
		}
		if err := p.Store.Save(p.SessionID, cur); err != nil {
			return Result{Content: "plan update_item save failed: " + err.Error(), IsError: true}, nil
		}
		p.fireEmit(ctx, cur, "update_item")
		return Result{Content: renderPlanResult("updated "+args.ID, cur)}, nil

	case "add_item":
		if args.Text == "" {
			return Result{Content: "add_item requires text", IsError: true}, nil
		}
		cur, err := p.Store.Load(p.SessionID)
		if err != nil {
			return Result{Content: "plan load failed: " + err.Error(), IsError: true}, nil
		}
		st := args.Status
		if st == "" {
			st = plan.Pending
		}
		cur.Items = append(cur.Items, plan.Item{
			Text:   args.Text,
			Status: st,
			Note:   args.Note,
		})
		if err := p.Store.Save(p.SessionID, cur); err != nil {
			return Result{Content: "plan add_item save failed: " + err.Error(), IsError: true}, nil
		}
		newID := cur.Items[len(cur.Items)-1].ID
		p.fireEmit(ctx, cur, "add_item")
		return Result{Content: renderPlanResult("added "+newID, cur)}, nil

	case "remove_item":
		if args.ID == "" {
			return Result{Content: "remove_item requires id", IsError: true}, nil
		}
		cur, err := p.Store.Load(p.SessionID)
		if err != nil {
			return Result{Content: "plan load failed: " + err.Error(), IsError: true}, nil
		}
		if !removeItem(cur, args.ID) {
			return Result{Content: fmt.Sprintf("plan remove_item: id %q not found", args.ID), IsError: true}, nil
		}
		if err := p.Store.Save(p.SessionID, cur); err != nil {
			return Result{Content: "plan remove_item save failed: " + err.Error(), IsError: true}, nil
		}
		p.fireEmit(ctx, cur, "remove_item")
		return Result{Content: renderPlanResult("removed "+args.ID, cur)}, nil

	default:
		return Result{Content: "unknown operation: " + args.Operation + " (must be set|update_item|add_item|remove_item|list)", IsError: true}, nil
	}
}

// fireEmit invokes the optional emitter; protected against nil.
func (p *Plan) fireEmit(ctx context.Context, pl *plan.Plan, op string) {
	if p.Emit != nil {
		p.Emit(ctx, pl, op)
	}
}

// mutateItem walks p.Items (and one sub level) and applies fn to the
// item whose ID matches. Returns true on hit. The walk is bounded to one
// sub level because Plan.Normalize rejects deeper nesting.
func mutateItem(p *plan.Plan, id string, fn func(*plan.Item)) bool {
	for i := range p.Items {
		if p.Items[i].ID == id {
			fn(&p.Items[i])
			return true
		}
		for j := range p.Items[i].Sub {
			if p.Items[i].Sub[j].ID == id {
				fn(&p.Items[i].Sub[j])
				return true
			}
		}
	}
	return false
}

// removeItem deletes the matching id (top-level OR sub-level). Returns
// true on hit. Walks once, mirroring mutateItem.
func removeItem(p *plan.Plan, id string) bool {
	for i := range p.Items {
		if p.Items[i].ID == id {
			p.Items = append(p.Items[:i], p.Items[i+1:]...)
			return true
		}
		for j := range p.Items[i].Sub {
			if p.Items[i].Sub[j].ID == id {
				p.Items[i].Sub = append(p.Items[i].Sub[:j], p.Items[i].Sub[j+1:]...)
				return true
			}
		}
	}
	return false
}

// renderPlanResult returns the "Plan {op}; current state:\n..." string
// the model sees as tool_result content. The format is human-readable
// but stable enough for tests to assert against.
func renderPlanResult(opMsg string, p *plan.Plan) string {
	pen, ip, comp := p.Counts()
	header := fmt.Sprintf("Plan %s. (%d pending · %d in_progress · %d completed; v%d)\n", opMsg, pen, ip, comp, p.Version)
	if len(p.Items) == 0 {
		return header + "(plan is empty)"
	}
	var body string
	for _, it := range p.Items {
		body += fmt.Sprintf("- [%s] %s: %s", planMark(it.Status), it.ID, it.Text)
		if it.Note != "" {
			body += " (" + it.Note + ")"
		}
		body += "\n"
		for _, sub := range it.Sub {
			body += fmt.Sprintf("    - [%s] %s: %s\n", planMark(sub.Status), sub.ID, sub.Text)
		}
	}
	return header + body
}

// planMark is the ASCII status indicator used inside the tool_result
// body. The TUI/CLI use Unicode glyphs (✓ ● ○) when rendering for the
// human; here we keep ASCII so the model sees the same shape regardless
// of locale.
func planMark(s plan.Status) string {
	switch s {
	case plan.Completed:
		return "x"
	case plan.InProgress:
		return "~"
	default:
		return " "
	}
}
