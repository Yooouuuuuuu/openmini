//go:build !windows

package agyapi

import "errors"

// keyringToken is only implemented for Windows; elsewhere agy writes a file
// whenever the desktop keyring is unavailable (WSL, servers, containers).
func keyringToken(string) ([]byte, error) {
	return nil, errors.New("no OS keyring reader on this platform")
}
