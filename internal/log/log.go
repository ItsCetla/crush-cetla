package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/term"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	initOnce    sync.Once
	initialized atomic.Bool
	logDir      atomic.Pointer[string]
)

func Setup(logFile string, debug bool, ws ...io.Writer) {
	initOnce.Do(func() {
		if logFile != "" {
			dir := filepath.Dir(logFile)
			logDir.Store(&dir)
		}

		logRotator := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    10,    // Max size in MB
			MaxBackups: 0,     // Number of backups
			MaxAge:     30,    // Days
			Compress:   false, // Enable compression
		}

		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}

		opts := &slog.HandlerOptions{
			Level:     level,
			AddSource: true,
		}

		var handlers []slog.Handler
		handlers = append(handlers, slog.NewJSONHandler(logRotator, opts))

		for _, w := range ws {
			if w == nil {
				continue
			}
			if f, ok := w.(term.File); ok && term.IsTerminal(f.Fd()) {
				handlers = append(handlers, slog.NewTextHandler(w, opts))
			} else {
				handlers = append(handlers, slog.NewJSONHandler(w, opts))
			}
		}

		slog.SetDefault(slog.New(slog.NewMultiHandler(handlers...)))
		initialized.Store(true)
	})
}

func Initialized() bool {
	return initialized.Load()
}

func RecoverPanic(name string, cleanup func()) {
	if r := recover(); r != nil {
		file, err := createPanicLog(name)
		if err == nil {
			defer file.Close()

			// Write panic information and stack trace
			fmt.Fprintf(file, "Panic in %s: %v\n\n", name, r)
			fmt.Fprintf(file, "Time: %s\n\n", time.Now().Format(time.RFC3339))
			fmt.Fprintf(file, "Stack Trace:\n%s\n", debug.Stack())

			// Execute cleanup function if provided
			if cleanup != nil {
				cleanup()
			}
		}
	}
}

// createPanicLog creates a timestamped panic log file next to the Ta-Da log,
// or in the system temp directory when that is not available. CreateTemp
// makes the file readable only by the user and never reuses an existing path.
func createPanicLog(name string) (*os.File, error) {
	pattern := fmt.Sprintf("tada-panic-%s-%s-*.log", name, time.Now().Format("20060102-150405"))
	if dir := logDir.Load(); dir != nil {
		if file, err := os.CreateTemp(*dir, pattern); err == nil {
			return file, nil
		}
	}
	return os.CreateTemp(os.TempDir(), pattern)
}
