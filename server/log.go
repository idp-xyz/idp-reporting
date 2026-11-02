package server

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// logger writes one JSON object per line. What goes in a line is chosen at the call site from counts, codes and fixed words: there is no
// call that takes an error, a request, a header, a subject, a tenant or a configuration value.
type logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func newLogger(w io.Writer, now func() time.Time) *logger {
	if w == nil {
		w = io.Discard
	}
	return &logger{w: w, now: now}
}

func (l *logger) log(level, event string, fields map[string]any) {
	line := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		line[k] = v
	}
	line["ts"] = l.now().UTC().Format(time.RFC3339Nano)
	line["level"] = level
	line["event"] = event
	b, err := json.Marshal(line)
	if err != nil {
		b = []byte(`{"level":"error","event":"log_failed"}`)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

func (l *logger) info(event string, fields map[string]any)  { l.log("info", event, fields) }
func (l *logger) warn(event string, fields map[string]any)  { l.log("warn", event, fields) }
func (l *logger) error(event string, fields map[string]any) { l.log("error", event, fields) }
