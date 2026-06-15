package logutil

import (
	"context"
	"log/slog"
	"sync"
)

// LogLevel represents the level of a log entry.

// LogEntry represents a captured log entry for comparison.
type LogEntry struct {
	Level   slog.Level
	Message string
	Args    []any
}

// CaptureLogger is a [slog.Handler] implementation that captures all
// submitted [slog.Record]s and stored them within an internalized struct.
//
// This logger is thread safe.
type CaptureLogger struct {
	Entries []slog.Record
	Handler slog.Handler
	Level   slog.Leveler
	sync.Mutex
}

var _ slog.Handler = (*CaptureLogger)(nil)

func (c *CaptureLogger) Enabled(ctx context.Context, level slog.Level) bool {
	minLevel := slog.LevelDebug
	if c.Level != nil {
		minLevel = c.Level.Level()
	}
	return level >= minLevel
}

// Handle implements [slog.Handler].
func (c *CaptureLogger) Handle(ctx context.Context, record slog.Record) error {
	c.Lock()
	c.Entries = append(c.Entries, record)
	c.Unlock()
	return c.Handler.Handle(ctx, record)
}

// WithAttrs implements [slog.Handler].
//
// We ignore calls to WithAttrs and WithGroup since we want to capture all
// log entries, and these functions are used to create new handlers with
// additional context, which we don't need for our purposes.
func (c *CaptureLogger) WithAttrs(attrs []slog.Attr) slog.Handler {
	return c
}

// WithGroup implements [slog.Handler].
//
// We ignore calls to WithAttrs and WithGroup since we want to capture all
// log entries, and these functions are used to create new handlers with
// additional context, which we don't need for our purposes.
func (c *CaptureLogger) WithGroup(name string) slog.Handler {
	return c
}

// NewCaptureLogger creates a new instance of [CaptureLogger].
func NewCaptureLogger(handler slog.Handler) *CaptureLogger {
	if handler == nil {
		handler = slog.DiscardHandler
	}
	return &CaptureLogger{
		Handler: handler,
	}
}
