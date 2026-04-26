package event

import "time"

// Source는 이벤트 출처를 나타낸다.
type Source int

const (
	SourceUnspecified Source = iota
	SourceAgent
	SourceUser
	SourceEnvironment
	SourceSystem
)

// Kind는 이벤트 종류를 나타낸다.
type Kind int

const (
	KindUnspecified Kind = iota
	KindAction
	KindObservation
	KindNote
)

// Event는 단일 이벤트의 메모리 표현이다.
type Event struct {
	ID        int64
	Timestamp time.Time
	Source    Source
	Kind      Kind
	Type      string
	Data      []byte // JSON
	Cause     int64
	Metrics   Metrics
}

type Metrics struct {
	Tokens    int64
	CostUSD   float64
	LatencyMs int64
}
