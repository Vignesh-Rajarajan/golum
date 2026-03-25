// Package applog provides optional file logging for debugging TUI issues (panics, stream errors).
// Set GOLUM_LOG to a file path to append logs there (directory is created as needed).
//
// Important: call Init() after godotenv (or config.Load) so GOLUM_LOG from a .env file is visible.
//
// When GOLUM_LOG is set, logs go only to the file—not the terminal—so Bubble Tea’s UI is not corrupted.
// With no GOLUM_LOG, runtime logs are discarded: writing stderr while the TUI holds the terminal
// interleaves with the alt-screen redraw and can make log lines appear on the input/prompt line.
// Use GOLUM_LOG when you need a trace (e.g. /tmp/golum.log).
package applog

import (
	"fmt"
	"io"
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
		std = log.New(io.Discard, prefix, flags)
		return
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0750); err != nil {
			fmt.Fprintf(os.Stderr, "golum applog: mkdir %s: %v\n", dir, err)
			std = log.New(io.Discard, prefix, flags)
			return
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "golum applog: open %s: %v\n", path, err)
		std = log.New(io.Discard, prefix, flags)
		return
	}
	fileW := syncFileWriter{f: f}
	// Do not tee to stderr: writing stderr while bubbletea holds the terminal corrupts the TUI.
	std = log.New(fileW, prefix, flags)
	std.Printf("logging to %s (GOLUM_LOG=%q)\n", path, path)
}

// Logger returns the configured logger (discard, file, or as set by Init).
func Logger() *log.Logger {
	if std == nil {
		Init()
	}
	return std
}

// Printf writes a log line (to GOLUM_LOG file when set; otherwise discarded).
func Printf(format string, v ...any) {
	Logger().Printf(format, v...)
}

// LogPanic writes stack trace to stderr (never the discard logger). Does not re-panic.
func LogPanic(scope string, r any) {
	fmt.Fprintf(os.Stderr, "golum panic in %s: %v\n%s", scope, r, string(debug.Stack()))
}

// RecoverMain is meant for `defer RecoverMain()` at the top of main.
func RecoverMain() {
	if r := recover(); r != nil {
		Init()
		LogPanic("main", r)
		os.Exit(1)
	}
}
