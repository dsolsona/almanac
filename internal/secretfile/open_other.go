//go:build !unix

package secretfile

import "os"

func open(path string) (*os.File, error) { return os.Open(path) }
