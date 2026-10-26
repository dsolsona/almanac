//go:build unix

package secretfile

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadRejectsFIFOsWithoutBlocking(t *testing.T) {
	for _, test := range []struct {
		name string
		read func(string) ([]byte, error)
	}{{"secret", func(path string) ([]byte, error) { return Read(path, 16) }}, {"certificate", ReadCertificate}} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pipe")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := test.read(path); err == nil {
				t.Fatal("FIFO was accepted")
			}
		})
	}
}
