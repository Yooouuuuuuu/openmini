//go:build !windows

package ui

// Package ui: outside Windows there is no window, tray or console tweak; the
// server stays in the terminal it was started from.
func Run(title, url string, quit func(), sink func(func(string)), logf func(string, ...any)) bool {
	return false
}
func SpawnDetached(logDir string) error { return nil }
func Close()                            {}
