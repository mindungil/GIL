package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// setfrozen updates a session's status to "frozen" in the database.
// Usage: go run setfrozen.go <db-path> <session-id>
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: go run setfrozen.go <db-path> <session-id>")
		os.Exit(1)
	}

	dbPath := os.Args[1]
	sessionID := os.Args[2]

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	_, err = db.Exec("UPDATE sessions SET status='frozen' WHERE id=?", sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update status: %v\n", err)
		os.Exit(1)
	}
}
