There is a CSV line parser in `parser.go` with a known bug. One test in `parser_test.go` is failing — `TestParseLine_EscapedQuote`. The others are passing.

Your task:
1. Run `go test ./...` to confirm the current state (one failing test).
2. Read `parser.go` to understand the parser.
3. Fix the bug so that `TestParseLine_EscapedQuote` passes.
4. Make sure all other tests still pass — do NOT modify any test file.
5. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. Do not edit `parser_test.go` or change test assertions.
