package watchers

import "time"

type Event struct {
	Source    string         `json:"source"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	Timestamp time.Time      `json:"timestamp"`
}

type Watcher interface {
	Start(events chan<- Event) error
	Stop() error
	Name() string
}
