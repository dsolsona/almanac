//go:build !linux

package cliprofile

import (
	"context"
	"errors"
)

var (
	ErrInvalid         = errors.New("invalid CLI profile")
	ErrUnavailable     = errors.New("CLI profile unavailable")
	ErrCommitUncertain = errors.New("CLI profile commit outcome uncertain")
	ErrUnsupported     = errors.New("CLI profile storage unsupported")
)

type Record struct {
	SchemaVersion int    `json:"schema_version"`
	BackendURL    string `json:"backend_url"`
	CAPEM         string `json:"ca_pem"`
	Session       string `json:"session"`
}

type Locked struct{}

func Open(context.Context, string, bool) (*Locked, error) { return nil, ErrUnsupported }
func (*Locked) Load() (Record, bool, error)               { return Record{}, false, ErrUnsupported }
func (*Locked) Save(Record) error                         { return ErrUnsupported }
func (*Locked) Close() error                              { return nil }
