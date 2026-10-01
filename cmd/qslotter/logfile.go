package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
)

var logFile *os.File

// setupLog sends the log to a file as well as stderr: a windowed app (no
// console on Windows, started from the Dock on macOS) has nowhere else to
// write. Runtime crash reports go to the file too. Returns the log path (""
// if no file could be opened). Rotation happens later, in rotateLog, once
// this process is known to be the only instance.
func setupLog() string {
	dir := logDir()
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		return ""
	}
	path := filepath.Join(dir, "qslotter.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return ""
	}
	logFile = f
	log.SetOutput(teeWriter{f})
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	return path
}

// rotateLog starts a fresh log file when the current one is over 5 MB. Only
// the primary instance calls it: a second launch (which just hands over to
// the running one) must not rename the file the primary is writing.
func rotateLog(path string) {
	if path == "" || logFile == nil {
		return
	}
	fi, err := logFile.Stat()
	if err != nil || fi.Size() < 5<<20 {
		return
	}
	_ = logFile.Close()
	_ = os.Rename(path, path+".1")
	setupLog()
}

// teeWriter writes to the log file and, best effort, to stderr. Not
// io.MultiWriter: that stops at the first error, and a windowed exe (no
// console on Windows) fails every stderr write - the file would get nothing.
type teeWriter struct{ f io.Writer }

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.f.Write(p)
	_, _ = os.Stderr.Write(p)
	return n, err
}

// logDir: %LOCALAPPDATA%\qslotter on Windows, ~/Library/Logs/qslotter on
// macOS, the XDG cache dir (~/.cache/qslotter) elsewhere.
func logDir() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Logs", "qslotter")
		}
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "qslotter")
	}
	return ""
}
