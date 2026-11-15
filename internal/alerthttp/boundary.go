package alerthttp

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func decodeBody(r *http.Request, target any) int {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return 400
	}
	media, params, err := mime.ParseMediaType(values[0])
	if err != nil || media != "application/json" || len(r.Header.Values("Content-Encoding")) != 0 || !(len(params) == 0 || len(params) == 1 && strings.EqualFold(params["charset"], "utf-8")) {
		return 415
	}
	if r.Body == nil {
		return 400
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil {
		return 400
	}
	if len(body) > 64<<10 {
		return 413
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, target, json.RejectUnknownMembers(true)) != nil {
		return 400
	}
	var members map[string]jsontext.Value
	if json.Unmarshal(body, &members) != nil {
		return 400
	}
	for _, value := range members {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return 400
		}
	}
	return 0
}

func etag(kind, id string, version int64) string {
	return `"` + kind + ":" + id + ":" + strconv.FormatInt(version, 10) + `"`
}

func parseETag(values []string, kind, id string) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	value, ok := strings.CutPrefix(values[0], `"`+kind+":"+id+":")
	if !ok {
		return 0, false
	}
	value, ok = strings.CutSuffix(value, `"`)
	if !ok {
		return 0, false
	}
	version, err := strconv.ParseInt(value, 10, 64)
	return version, err == nil && version > 0 && strconv.FormatInt(version, 10) == value
}

func printableHeader(r *http.Request, name string) bool {
	values := r.Header.Values(name)
	if len(values) != 1 || len(values[0]) < 1 || len(values[0]) > 128 {
		return false
	}
	for _, value := range values[0] {
		if value < 33 || value > 126 {
			return false
		}
	}
	return true
}

func pageQuery(query url.Values, allowed ...string) (int, string, bool) {
	limit, cursor := 20, ""
	for key, values := range query {
		if len(values) != 1 || values[0] == "" || !utf8.ValidString(values[0]) || strings.ContainsFunc(values[0], unicode.IsControl) {
			return 0, "", false
		}
		switch key {
		case "limit":
			value, err := strconv.Atoi(values[0])
			if err != nil || value < 1 || value > 200 || strconv.Itoa(value) != values[0] {
				return 0, "", false
			}
			limit = value
		case "cursor":
			if len(values[0]) > 1024 {
				return 0, "", false
			}
			cursor = values[0]
		default:
			if !slices.Contains(allowed, key) || key == "q" && utf8.RuneCountInString(values[0]) > 256 {
				return 0, "", false
			}
		}
	}
	return limit, cursor, true
}

func noReadBody(r *http.Request) bool {
	return r.ContentLength == 0 && len(r.TransferEncoding) == 0 && len(r.Header.Values("If-Match")) == 0 && len(r.Header.Values("If-None-Match")) == 0 && len(r.Header.Values("Idempotency-Key")) == 0
}
