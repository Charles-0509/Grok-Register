package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Log level for display filters (file always stores full detail).
const (
	LevelDebug = 10
	LevelInfo  = 20
	LevelWarn  = 30
	LevelError = 40
)

type Logger struct {
	mu   sync.Mutex
	out  io.Writer
	file *os.File
}

func New(path string) (*Logger, error) {
	l := &Logger{out: os.Stdout}
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		l.file = f
		l.out = io.MultiWriter(os.Stdout, f)
	}
	return l, nil
}

func (l *Logger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

func (l *Logger) log(prefix, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := time.Now().Format("15:04:05")
	_, _ = fmt.Fprintf(l.out, "[%s] %s %s\n", ts, prefix, msg)
}

func (l *Logger) Info(msg string)  { l.log("INFO", msg) }
func (l *Logger) OK(msg string)    { l.log("✓", msg) }
func (l *Logger) Start(msg string) { l.log("→", msg) }
func (l *Logger) Warn(msg string)  { l.log("!", msg) }
func (l *Logger) Err(msg string)   { l.log("✗", msg) }
func (l *Logger) Debug(msg string) { l.log("DBG", msg) }

func (l *Logger) Infof(f string, a ...any)  { l.Info(fmt.Sprintf(f, a...)) }
func (l *Logger) OKf(f string, a ...any)    { l.OK(fmt.Sprintf(f, a...)) }
func (l *Logger) Startf(f string, a ...any) { l.Start(fmt.Sprintf(f, a...)) }
func (l *Logger) Warnf(f string, a ...any)  { l.Warn(fmt.Sprintf(f, a...)) }
func (l *Logger) Errf(f string, a ...any)   { l.Err(fmt.Sprintf(f, a...)) }
func (l *Logger) Debugf(f string, a ...any) { l.Debug(fmt.Sprintf(f, a...)) }

// ParseLevel maps CLI flags / names to a numeric threshold.
// Default (empty) is LevelInfo — hides DBG when viewing logs.
func ParseLevel(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "--")
	switch s {
	case "", "info", "i":
		return LevelInfo, nil
	case "debug", "dbg", "d", "all", "trace", "v", "verbose":
		return LevelDebug, nil
	case "warn", "warning", "w":
		return LevelWarn, nil
	case "error", "err", "e", "fatal":
		return LevelError, nil
	default:
		return 0, fmt.Errorf("未知日志等级: %s（可用 debug|info|warn|error）", s)
	}
}

// PrefixLevel maps a log line tag (INFO/DBG/✓/→/!/✗) to severity.
func PrefixLevel(prefix string) int {
	switch strings.TrimSpace(prefix) {
	case "DBG", "DEBUG":
		return LevelDebug
	case "INFO", "✓", "→", "OK", "START":
		return LevelInfo
	case "!", "WARN", "WARNING":
		return LevelWarn
	case "✗", "ERR", "ERROR", "FATAL":
		return LevelError
	default:
		// unknown / continuation: treat as info so default view still shows it
		return LevelInfo
	}
}

// LineLevel extracts severity from a full log line: "[15:04:05] PREFIX rest"
func LineLevel(line string) int {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return LevelInfo
	}
	// strip optional timestamp [HH:MM:SS]
	rest := line
	if strings.HasPrefix(rest, "[") {
		if i := strings.Index(rest, "]"); i >= 0 {
			rest = strings.TrimSpace(rest[i+1:])
		}
	}
	if rest == "" {
		return LevelInfo
	}
	// first token is prefix
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return LevelInfo
	}
	return PrefixLevel(fields[0])
}

// KeepLine reports whether line should be shown at minLevel (inclusive of higher severity).
func KeepLine(line string, minLevel int) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	return LineLevel(line) >= minLevel
}

// FilterText keeps only lines at or above minLevel.
func FilterText(text string, minLevel int) string {
	if minLevel <= LevelDebug {
		return text
	}
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		// Split drops final empty; re-join carefully
		if line == "" && !strings.HasSuffix(text, "\n") {
			continue
		}
		if KeepLine(line, minLevel) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
