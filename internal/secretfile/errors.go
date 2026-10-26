package secretfile

import "errors"

var (
	ErrTokenCancelled       = errors.New("token output cancelled")
	ErrTokenCommitUncertain = errors.New("token output commit could not be confirmed")
)
