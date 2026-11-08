package storage

import (
	"encoding/base64"
	"encoding/json/v2"
)

type serviceSortCursor struct {
	Version int    `json:"version"`
	Filter  string `json:"filter"`
	Number  int64  `json:"number"`
	Text    string `json:"text"`
	ID      string `json:"id"`
}

func decodeServiceSortCursor(value, filter string) (serviceSortCursor, error) {
	var position serviceSortCursor
	if len(value) > 2048 {
		return position, ErrObjectiveInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value || json.Unmarshal(raw, &position, json.RejectUnknownMembers(true)) != nil || position.Version != 1 || position.Filter != filter || position.Number < 0 || !serviceText(position.Text, 200, false) || !canonicalUUIDCheck(position.ID) {
		return serviceSortCursor{}, ErrObjectiveInvalid
	}
	return position, nil
}

func encodeServiceSortCursor(position serviceSortCursor) (string, error) {
	raw, err := json.Marshal(position)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
