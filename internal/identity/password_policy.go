// Package identity contains bounded password and opaque session-secret
// primitives used by the identity service.
package identity

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"unicode/utf8"
)

// ErrCommonPassword reports a password that exactly matches an entry in the
// bundled common-password list.
var ErrCommonPassword = errors.New("password is too common")

// ErrPasswordPolicyUnavailable reports that the bundled password policy data
// could not be loaded or validated.
var ErrPasswordPolicyUnavailable = errors.New("password policy unavailable")

const (
	passwordListPath       = "passworddata/common-passwords.txt"
	passwordListSHA256     = "c2e5696882c603b76bb67a47ee970897e5a76fc4c3f5547abe3d0ca340c576e0"
	passwordListEntryCount = 99840
)

//go:embed passworddata
var passwordData embed.FS

var loadPasswordList = sync.OnceValues(func() (map[string]struct{}, error) {
	data, err := fs.ReadFile(passwordData, passwordListPath)
	if err != nil {
		return nil, ErrPasswordPolicyUnavailable
	}
	return validatePasswordList(data)
})

func validatePasswordList(data []byte) (map[string]struct{}, error) {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != passwordListSHA256 {
		return nil, ErrPasswordPolicyUnavailable
	}
	entries, err := parsePasswordList(data)
	if err != nil || len(entries) != passwordListEntryCount {
		return nil, ErrPasswordPolicyUnavailable
	}
	return entries, nil
}

// ValidateNewPassword applies the text policy and rejects exact whole-value
// matches from the bundled offline common-password list. It does not alter the
// supplied text or affect verification of credentials created earlier.
func ValidateNewPassword(password string) error {
	if err := ValidatePasswordText(password); err != nil {
		return err
	}
	entries, err := loadPasswordList()
	if err != nil {
		return ErrPasswordPolicyUnavailable
	}
	if _, ok := entries[password]; ok {
		return ErrCommonPassword
	}
	return nil
}

func parsePasswordList(data []byte) (map[string]struct{}, error) {
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, ErrPasswordPolicyUnavailable
	}
	entries := make(map[string]struct{}, passwordListEntryCount)
	for len(data) > 0 {
		line, rest, hasNewline := bytes.Cut(data, []byte{'\n'})
		data = rest
		// The pinned upstream file includes one empty line as an entry. Preserve
		// it byte-for-byte; structural password validation makes it unmatchable.
		if bytes.ContainsRune(line, '\r') || bytes.IndexByte(line, 0) >= 0 {
			return nil, ErrPasswordPolicyUnavailable
		}
		entry := string(line)
		if _, exists := entries[entry]; exists {
			return nil, ErrPasswordPolicyUnavailable
		}
		entries[entry] = struct{}{}
		if !hasNewline {
			break
		}
		if len(data) == 0 {
			break
		}
	}
	return entries, nil
}
