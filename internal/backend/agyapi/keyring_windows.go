//go:build windows

package agyapi

import (
	"unicode/utf16"

	"github.com/danieljoos/wincred"
)

// keyringToken returns agy's stored session from the Windows Credential
// Manager, where the CLI keeps it on Windows (generic credential
// "gemini:antigravity"; there is no token file there).
func keyringToken(target string) ([]byte, error) {
	c, err := wincred.GetGenericCredential(target)
	if err != nil {
		return nil, err
	}
	b := c.CredentialBlob
	if len(b) >= 2 && b[1] == 0 { // stored as UTF-16LE by some writers
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return []byte(string(utf16.Decode(u))), nil
	}
	return b, nil
}
