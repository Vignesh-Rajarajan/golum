// Package applog provides optional file logging for debugging TUI issues (panics, stream errors).
// Set GOLUM_LOG to a file path to append logs there (directory is created as needed).
//
// Important: call Init() after godotenv (or config.Load) so GOLUM_LOG from a .env file is visible.
//
// When GOLUM_LOG is set, logs go only to the file—not stderr—so Bubble Tea’s raw terminal UI is not corrupted.
// With no GOLUM_LOG, logs go to stderr only (useful when running without a file).
package applog

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
)

var std *log.Logger

// syncFileWriter flushes to disk after each write so logs appear while the TUI holds the terminal.
type syncFileWriter struct{ f *os.File }

func (w syncFileWriter) Write(p []byte) (n int, err error) {
	n, err = w.f.Write(p)
	if err == nil {
		_ = w.f.Sync()
	}
	return n, err
}

// Init configures logging. Safe to call multiple times; first successful file open wins.
func Init() {
	if std != nil {
		return
	}
	flags := log.LstdFlags | log.Lmicroseconds
	prefix := "golum "
	path := os.Getenv("GOLUM_LOG")
	if path == "" {
		std = log.New(os.Stderr, prefix, flags)
		return
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0750); err != nil {
			std = log.New(os.Stderr, prefix, flags)
			std.Printf("applog: mkdir %s: %v", dir, err)
			return
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		std = log.New(os.Stderr, prefix, flags)
		std.Printf("applog: open %s: %v", path, err)
		return
	}
	fileW := syncFileWriter{f: f}
	// Do not tee to stderr: writing stderr while bubbletea holds the terminal corrupts the TUI.
	std = log.New(fileW, prefix, flags)
	std.Printf("logging to %s (GOLUM_LOG=%q)\n", path, path)
}

// Logger returns the configured logger, initializing to stderr if needed.
func Logger() *log.Logger {
	if std == nil {
		Init()
	}
	return std
}

// Printf writes a log line (file only when GOLUM_LOG is set; else stderr).
func Printf(format string, v ...any) {
	Logger().Printf(format, v...)
}

// LogPanic writes stack trace to the log (and stderr). Does not re-panic.
func LogPanic(scope string, r any) {
	Logger().Printf("panic in %s: %v\n%s", scope, r, string(debug.Stack()))
}

// RecoverMain is meant for `defer RecoverMain()` at the top of main.
func RecoverMain() {
	if r := recover(); r != nil {
		Init()
		LogPanic("main", r)
		os.Exit(1)
	}
}
