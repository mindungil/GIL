package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persister writes events to a JSONL append-only file.
type Persister struct {
	mu     sync.Mutex
	file   *os.File
	w      *bufio.Writer
	closed bool
}

// jsonEvent is the JSON serialization format (no proto, human-readable).
type jsonEvent struct {
	ID        int64   `json:"id"`
	Timestamp string  `json:"timestamp"`
	Source    int     `json:"source"`
	Kind      int     `json:"kind"`
	Type      string  `json:"type"`
	Data      string  `json:"data,omitempty"`
	Cause     int64   `json:"cause,omitempty"`
	Tokens    int64   `json:"tokens,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	LatencyMs int64   `json:"latency_ms,omitempty"`
}

// NewPersister creates a new persister that appends events to a JSONL file.
func NewPersister(dir string) (*Persister, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Persister{file: f, w: bufio.NewWriter(f)}, nil
}

// Write appends an event to the JSONL file.
func (p *Persister) Write(e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	je := jsonEvent{
		ID:        e.ID,
		Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
		Source:    int(e.Source),
		Kind:      int(e.Kind),
		Type:      MaskSecrets(e.Type),
		Data:      MaskSecrets(string(e.Data)),
		Cause:     e.Cause,
		Tokens:    e.Metrics.Tokens,
		CostUSD:   e.Metrics.CostUSD,
		LatencyMs: e.Metrics.LatencyMs,
	}
	b, err := json.Marshal(je)
	if err != nil {
		return err
	}
	if _, err := p.w.Write(b); err != nil {
		return err
	}
	return p.w.WriteByte('\n')
}

// Sync flushes buffered writes to disk.
func (p *Persister) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.w.Flush(); err != nil {
		return err
	}
	return p.file.Sync()
}

// Close closes the persister and releases the file handle.
func (p *Persister) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.w != nil {
		_ = p.w.Flush()
	}
	return p.file.Close()
}

// LoadAll reads all events from a JSONL file.
func LoadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Event
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var je jsonEvent
			if jerr := json.Unmarshal(line, &je); jerr != nil {
				return nil, jerr
			}
			ts, err := time.Parse(time.RFC3339Nano, je.Timestamp)
			if err != nil {
				return nil, fmt.Errorf("event %d: invalid timestamp %q: %w", je.ID, je.Timestamp, err)
			}
			out = append(out, Event{
				ID:        je.ID,
				Timestamp: ts,
				Source:    Source(je.Source),
				Kind:      Kind(je.Kind),
				Type:      je.Type,
				Data:      []byte(je.Data),
				Cause:     je.Cause,
				Metrics: Metrics{
					Tokens:    je.Tokens,
					CostUSD:   je.CostUSD,
					LatencyMs: je.LatencyMs,
				},
			})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
}
