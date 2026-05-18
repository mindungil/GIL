Write a Go module in the current working directory that validates a JSON document against an inline schema.

Build a CLI named `jsonval` that:
- Reads the JSON document from stdin
- Takes a schema specification via `--schema <path>` (JSON file with fields described below)
- Exits 0 if valid, 1 if invalid, 2 on internal error (e.g. malformed schema)
- Prints validation errors (one per line) to stderr on exit 1

Schema format (own simple DSL, not full JSON Schema):
```json
{
  "type": "object",
  "fields": {
    "name":   { "type": "string", "required": true, "min_len": 1 },
    "age":    { "type": "number", "required": true, "min": 0, "max": 150 },
    "tags":   { "type": "array",  "required": false, "item_type": "string" },
    "active": { "type": "boolean", "required": true }
  }
}
```

Supported field types: `string` (with optional `min_len`, `max_len`), `number` (with optional `min`, `max`), `boolean`, `array` (with optional `item_type`). `required` defaults to true.

Include `go test ./...` covering at minimum:
1. Valid document passes
2. Missing required field fails with named error
3. Wrong type fails
4. Number out of range fails
5. Array item type mismatch fails

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
