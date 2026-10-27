// Package secretfile reads protected, bounded regular files.
package secretfile

import (
	"errors"
	"io"
)

var ErrInvalid = errors.New("secret file is invalid")

const MaxPrivateFileBytes = 128 << 10

// Read returns bytes from a regular file with no group or other permission bits.
func Read(path string, maxBytes int64) ([]byte, error) {
	if path == "" || maxBytes < 1 || maxBytes > MaxPrivateFileBytes {
		return nil, ErrInvalid
	}
	file, err := open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, ErrInvalid
	}
	return data, nil
}

// ReadCertificate reads a public certificate chain from a regular file. It uses
// the same opened-target and nonblocking checks as Read, but permits public modes.
func ReadCertificate(path string) ([]byte, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	file, err := open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > (64<<10) {
		return nil, ErrInvalid
	}
	return data, nil
}
