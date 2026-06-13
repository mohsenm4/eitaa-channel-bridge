package utils

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// LogLevelEnv is the env var read by NewLogger (debug|info|warn|error).
const LogLevelEnv = "LOG_LEVEL"

func NewLogger() *slog.Logger {
	level := slog.LevelInfo
	if s := os.Getenv(LogLevelEnv); s != "" {
		switch strings.ToLower(s) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.String("t", a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})
	return slog.New(h)
}

func Fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
