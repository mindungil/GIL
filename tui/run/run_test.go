package run

import (
	"context"
	"testing"
	"time"
)

func TestChat_DialFails_ReturnsErr(t *testing.T) {
	// /tmp/nonexistent.sock should fail to dial.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := Chat(ctx, "/tmp/gil-nonexistent-12345.sock")
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}
}
