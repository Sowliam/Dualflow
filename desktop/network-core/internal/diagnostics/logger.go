package diagnostics

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type LogLevel string

const (
	LevelDebug LogLevel = "DEBUG"
	LevelInfo  LogLevel = "INFO"
	LevelWarn  LogLevel = "WARN"
	LevelError LogLevel = "ERROR"
)

type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

var defaultLogger = &Logger{out: os.Stderr}

func GetLogger() *Logger {
	return defaultLogger
}

func (l *Logger) log(level LogLevel, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	// Write exclusively to stderr to avoid polluting stdout protocol communication
	fmt.Fprintf(l.out, "[CORE] [%s] [%s] %s\n", level, ts, msg)
}

func Debug(format string, args ...any) {
	defaultLogger.log(LevelDebug, format, args...)
}

func Info(format string, args ...any) {
	defaultLogger.log(LevelInfo, format, args...)
}

func Warn(format string, args ...any) {
	defaultLogger.log(LevelWarn, format, args...)
}

func Error(format string, args ...any) {
	defaultLogger.log(LevelError, format, args...)
}

