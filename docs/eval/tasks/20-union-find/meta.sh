TASK_NAME="union-find"
MAX_TURNS=15
MAX_WALL=20m
ASSERTS=(
    "find . -name '*_test.go' -type f | grep ."
    "timeout 60s go test ./..."
    "! timeout 60s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
)
