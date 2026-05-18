Implement an in-memory HTTP key-value service in Go in the current working directory.

API endpoints (all paths `/kv/{key}` with `{key}` being a URL path segment):
- `GET /kv/{key}` → 200 with JSON body `{"key": "...", "value": "..."}` if key exists, 404 if not
- `PUT /kv/{key}` (or `POST`) with JSON body `{"value": "..."}` → 200 on update, 201 on create; response body echoes `{"key":..., "value":...}`
- `DELETE /kv/{key}` → 204 on success, 404 if key didn't exist
- `GET /kv` → 200 with JSON body `{"keys": ["...", "..."]}` listing all keys (any order)

Service:
- Listen on a port chosen via `-addr :8080` flag (default :8080)
- In-memory storage (no file persistence)
- Concurrent-safe (use sync.RWMutex or similar)
- Build as a `kvserver` binary

Include `go test ./...` that spins up the handler in-process (use `httptest.NewServer`) and exercises at least:
1. PUT new key → 201 with body
2. GET existing key → 200 with body
3. GET missing key → 404
4. PUT existing key → 200 (update)
5. DELETE existing key → 204, then GET → 404
6. GET /kv → 200 with the current key list

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
