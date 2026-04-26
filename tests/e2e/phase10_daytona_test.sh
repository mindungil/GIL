#!/usr/bin/env bash
# Phase 10 e2e (Track B): Daytona driver REST round-trip under httptest fake.
#
# Approach: run the Go smoke binary at tests/e2e/daytona_smoke/, which:
#   1. Stands up an in-process httptest.Server speaking the Daytona REST
#      contract (POST /workspaces, POST /workspaces/{id}/exec, DELETE).
#   2. Drives the gil daytona Provider against it (DAYTONA_API_BASE points
#      at the httptest URL; DAYTONA_API_KEY=test-key gates Available()).
#   3. Exercises the FULL stack — Provider.Provision → Wrapper.ExecRemote
#      → bash tool's RemoteExecutor fast path → Teardown — and asserts the
#      exec/create/delete call counts plus the stdout that round-trips
#      through the fake server.
#
# Why a Go smoke binary instead of `go test`? It exits with a single 0/1
# the bash harness can act on, mirrors phase10_modal_test.sh's shape
# (build → run → assert), and stays decoupled from the runtime/ test
# package's lifecycle (which lives inside `make test`).
#
# Runs WITHOUT real Daytona credentials — DAYTONA_API_KEY=test-key is enough
# because Available() only checks env presence; no api.daytona.io contact.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

# Run the smoke from the server/ module so the workspace resolver sees
# both core/ and runtime/ deps. setfrozen.go uses the same trick.
cd "$ROOT/server"
go run "$ROOT/tests/e2e/daytona_smoke/main.go"

echo "OK: phase 10 e2e — Daytona REST driver under httptest"
