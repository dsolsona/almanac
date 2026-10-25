//go:build !unix

package command

import "os"

func openProtectedURLFile(path string) (*os.File, error) {
	return os.Open(path)
}
