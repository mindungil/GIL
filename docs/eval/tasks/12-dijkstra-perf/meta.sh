TASK_NAME="dijkstra-perf"
MAX_TURNS=25
MAX_WALL=30m
ASSERTS=(
    "find . -name '*_test.go' -type f | grep ."
    "timeout 60s go test ./..."
    "! timeout 60s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
)
