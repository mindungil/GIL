# Per-task config sourced by docs/eval/run-suite.sh.
TASK_NAME="md2html"
MAX_TURNS=12
MAX_WALL=15m
ASSERTS=(
    "find . -name '*_test.go' -type f | grep ."
    "timeout 60s go test ./..."
    "! timeout 60s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
)
