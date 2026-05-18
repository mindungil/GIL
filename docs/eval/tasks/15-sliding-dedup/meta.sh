TASK_NAME="sliding-dedup"
MAX_TURNS=25
MAX_WALL=30m
ASSERTS=(
    "find . -name '*_test.go' -type f | grep ."
    "timeout 60s go test -race ./..."
    "! timeout 60s go test -count=1 -race -v ./... 2>&1 | grep -q '^--- FAIL:'"
)
