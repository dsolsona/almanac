package storage

import (
	"encoding/json/v2"
	"math"
	"strconv"
	"time"
)

// Internal alert format 1 uses exact integer seconds for every duration. It is
// separate from public OpenAPI types and does not inherit legacy nanoseconds.
var alertDurationMarshalers = json.MarshalFunc(func(value time.Duration) ([]byte, error) {
	if value < 0 || value%time.Second != 0 {
		return nil, ErrAlertInvalid
	}
	return strconv.AppendInt(nil, int64(value/time.Second), 10), nil
})

var alertDurationUnmarshalers = json.UnmarshalFunc(func(data []byte, value *time.Duration) error {
	seconds, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
		return ErrAlertInvalid
	}
	*value = time.Duration(seconds) * time.Second
	return nil
})

func marshalAlertData(value any) ([]byte, error) {
	return json.Marshal(value, json.Deterministic(true), json.WithMarshalers(alertDurationMarshalers))
}
func unmarshalAlertData(data []byte, value any) error {
	return json.Unmarshal(data, value, json.WithUnmarshalers(alertDurationUnmarshalers))
}
