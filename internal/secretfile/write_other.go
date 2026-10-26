//go:build !linux

package secretfile

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("protected token output is unsupported on this platform")

type TokenFile struct{}

func Reserve(context.Context, string) (*TokenFile, error) { return nil, ErrUnsupported }
func (*TokenFile) Complete([]byte) error                  { return ErrUnsupported }
func (*TokenFile) Close() error                           { return nil }
func WriteNew(string, []byte) error                       { return ErrUnsupported }
