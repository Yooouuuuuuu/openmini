// Package logging writes one log file per day plus stdout, and deletes old
// files. It is used for ids, sizes, timings and status only, never for text.
package logging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	dir  string
	keep int
	day  string
	file *os.File
	std  *log.Logger
}

func New(dir string, keepDays int) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir, keep: keepDays, std: log.New(os.Stdout, "", log.LstdFlags)}
	if err := l.rotate(); err != nil {
		return nil, err
	}
	l.Housekeep()
	go func() {
		for range time.Tick(6 * time.Hour) {
			l.Housekeep()
		}
	}()
	return l, nil
}

func (l *Logger) rotate() error {
	day := time.Now().Format("2006-01-02")
	if day == l.day && l.file != nil {
		return nil
	}
	if l.file != nil {
		l.file.Close()
	}
	f, err := os.OpenFile(filepath.Join(l.dir, day+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file, l.day = f, day
	return nil
}

// Printf writes one line to today's file and to stdout.
func (l *Logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotate(); err == nil {
		fmt.Fprintf(l.file, "%s %s\n", time.Now().Format("2006/01/02 15:04:05"), fmt.Sprintf(format, args...))
	}
	l.std.Printf(format, args...)
}

// Housekeep deletes log files older than keep days.
func (l *Logger) Housekeep() {
	cutoff := time.Now().AddDate(0, 0, -l.keep)
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".log" {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(l.dir, e.Name()))
		}
	}
}
