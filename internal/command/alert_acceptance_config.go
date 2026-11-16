//go:build alertacceptance

package command

import (
	"errors"
	"time"
)

func alertAcceptanceRetention(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.IsZero() || parsed.Location() != time.UTC {
		return time.Time{}, errors.New("acceptance fixture requires explicit UTC retention start")
	}
	return parsed, nil
}
