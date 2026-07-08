package log

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ANSI color codes for terminal rendering.
// This is why logs look correct within Forge. We render them, not dump them as escaped strings.
const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// Event is a single structured log entry.
type Event struct {
	Timestamp  time.Time      `json:"ts"`
	Level      string         `json:"level"`
	StepID     string         `json:"step_id"`
	Message    string         `json:"message"`
	Fields     map[string]any `json:"fields,omitempty"`
	SourceFile string         `json:"source_file,omitempty"`
	SourceLine int            `json:"source_line,omitempty"`
}

// Logger writes structured log events for a specific step.
// Each step gets its own Logger, scoped to that step's ID.
type Logger struct {
	stepID  string
	logFile *os.File // structured JSON events written here
	secrets []string // secret values to scrub from all output

	// StreamCallback fires for every event immediately after it is written.
	// The agent sets this to forward lines to the scheduler in real time.
	// nil = no streaming (local runs).
	StreamCallback func(ts time.Time, level, message string)
}

// NewLogger creates a Logger for a step that writes events to logPath.
// The caller is responsible for calling Close() when done.
func NewLogger(stepID, logPath string) (*Logger, error) {
	f, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating log file: %w", err)
	}
	return &Logger{stepID: stepID, logFile: f}, nil
}

// RegisterSecrets tells the logger which values to redact from all output.
// Cal lthis after NewLogger, before any log writes, with the actual secret values
// (not names). The logger erplaces any occurrence with "***".
//
// This is the last line of defense against accidental secret leakage. For instance,
// a user printing their secret to a log file.
func (l *Logger) RegisterSecrets(values []string) {
	for _, v := range values {
		if v != "" {
			l.secrets = append(l.secrets, v)
		}
	}
}

// redact replaces all known secret values in s with "***"
func (l *Logger) redact(s string) string {
	for _, secret := range l.secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// Close flushes and closes the underlying log file.
// Defer this right after NewLogger
func (l *Logger) Close() error {
	return l.logFile.Close()
}

// Info logs an informational event.
func (l *Logger) Info(msg string, fields ...map[string]any) {
	l.write(LevelInfo, msg, fields...)
}

// Warn logs a warning event.
func (l *Logger) Warn(msg string, fields ...map[string]any) {
	l.write(LevelWarn, msg, fields...)
}

// Error logs an error event.
func (l *Logger) Error(msg string, fields ...map[string]any) {
	l.write(LevelError, msg, fields...)
}

// Output logs a raw line of command output (stdout/stderr from the step).
func (l *Logger) Output(line string) {
	l.write(LevelInfo, line, map[string]any{"stream": "stdout"})
}

// write is the internal implementation shared by all log methods.
// It does two things simultaneously.
// 1. Writes a JSON event to the log file (for later search/query).
// 2. Renders a colored human-readable line to the terminal.
func (l *Logger) write(level Level, msg string, fields ...map[string]any) {
	now := time.Now()

	msg = l.redact(msg)

	merged := map[string]any{}
	for _, f := range fields {
		for k, v := range f {
			merged[k] = v
		}
	}

	event := Event{
		Timestamp: now,
		Level:     level.String(),
		StepID:    l.stepID,
		Message:   msg,
		Fields:    merged,
	}
	if data, err := json.Marshal(event); err == nil {
		_, _ = l.logFile.Write(data)
		_, _ = l.logFile.WriteString("\n")
	}

	// Fire real-time stream callback (agent -> scheduler -> browser).
	// Non-blocking - if  no callback is set this is a no-op
	if l.StreamCallback != nil {
		l.StreamCallback(now, level.String(), msg)
	}

	levelColor := colorCyan
	switch level {
	case LevelWarn:
		levelColor = colorYellow
	case LevelError:
		levelColor = colorRed
	case LevelInfo:
		levelColor = colorGreen
	}

	fmt.Printf(
		"%s%s%s  %s%-5s%s  %s[%s]%s  %s\n",
		colorGray, now.Format("15:04:05.000"), colorReset,
		levelColor, level.String(), colorReset,
		colorBold, l.stepID, colorReset,
		msg,
	)
}

// StepHeader prints a visual separator before a step starts.
// Makes it easy to see where one step ends and another begins.
func StepHeader(stepID, image, command string) {
	fmt.Printf(
		"\n%s┌─ %s%s%s\n",
		colorCyan, colorBold, stepID, colorReset,
	)
	fmt.Printf("%s│%s  image:   %s\n", colorCyan, colorReset, image)
	fmt.Printf("%s│%s  command: %s\n", colorCyan, colorReset, command)
	fmt.Printf("%s└────────────────────────────────%s\n", colorCyan, colorReset)
}

// StepFooter prints a summary line after a step completes.
func StepFooter(stepID string, passed bool, duration time.Duration) {
	icon := "✓"
	color := colorGreen
	if !passed {
		icon = "✗"
		color = colorRed
	}
	fmt.Printf(
		"\n%s%s %s%s  %s(%s)%s\n",
		color, icon, stepID, colorReset,
		colorGray, duration.Round(time.Millisecond), colorReset,
	)
}
