//go:build !windows

package main

// Only Windows gets a window of its own; everywhere else the server stays in
// the terminal it was started from.
func runWindowed(title, url string, quit func(), sink func(func(string)), logf func(string, ...any)) bool {
	return false
}
func spawnDetached(logDir string) error { return nil }
func closeWindow()                      {}
