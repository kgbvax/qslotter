package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// setupLog sends the log to a file as well as stderr: a windowed app (no
// console on Windows, started from the Dock on macOS) has nowhere else to
// write. The file is rotated once at 5 MB. Returns the log path ("" if no
// file could be opened).
func setupLog() string {
	dir := logDir()
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	path := filepath.Join(dir, "qslotter.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ""
	}
	log.SetOutput(teeWriter{f})
	return path
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
