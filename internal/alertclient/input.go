package alertclient

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/dsolsona/almanac/internal/api"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"
)

const MaxInputBytes = 64 << 10

var decimal = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
var requestIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

func contains(v string, choices ...string) bool { return slices.Contains(choices, v) }
func validID(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id.String() == v && v != "00000000-0000-0000-0000-000000000000"
}
func counter(v string, positive bool) bool {
	n, err := strconv.ParseInt(v, 10, 64)
	return err == nil && n >= 0 && (!positive || n > 0) && strconv.FormatInt(n, 10) == v
}
func validRequestID(v string) bool { return requestIDPattern.MatchString(v) }
func ascii(v string, min, max int, space bool) bool {
	if len(v) < min || len(v) > max {
		return false
	}
	low := rune(33)
	if space {
		low = 32
	}
	for _, r := range v {
		if r < low || r > 126 {
			return false
		}
	}
	return true
}
func csv(v string, allowed ...string) bool {
	if v == "" {
		return true
	}
	seen := map[string]bool{}
	for s := range strings.SplitSeq(v, ",") {
		if !contains(s, allowed...) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func validName(v string) bool {
	if !utf8.ValidString(v) || len(v) > 1024 || utf8.RuneCountInString(v) > 256 || strings.TrimSpace(v) == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validThreshold(v string) bool {
	if len(v) > 32 || !decimal.MatchString(v) {
		return false
	}
	n, ok := new(big.Rat).SetString(v)
	return ok && n.Sign() > 0
}
func validConditions(v []api.AlertCondition) bool {
	if len(v) < 1 || len(v) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range v {
		if !ascii(c.Id, 1, 64, true) || strings.TrimSpace(c.Id) == "" || seen[c.Id] || c.ShortWindowSeconds < 60 || c.ShortWindowSeconds%60 != 0 || c.LongWindowSeconds < c.ShortWindowSeconds || c.LongWindowSeconds > 2592000 || c.LongWindowSeconds%60 != 0 || !validThreshold(c.Threshold) {
			return false
		}
		seen[c.Id] = true
	}
	return true
}
func ValidETag(tag, kind, id string) bool {
	v, ok := strings.CutPrefix(tag, `"`+kind+":"+id+":")
	if !ok {
		return false
	}
	v, ok = strings.CutSuffix(v, `"`)
	return ok && counter(v, true)
}

// Validate is also used before CLI profile access, so malformed commands do not
// authenticate or touch the network. Authority/cadence checks remain server-owned.
func Validate(in Input) error {
	if err := ValidateOptions(in); err != nil {
		return err
	}
	if in.Mutation() && !validBody(in) {
		return failure(in, "invalid_request", 0, false)
	}
	return nil
}

// ValidateOptions checks flags before the CLI reads a protected input file.
func ValidateOptions(in Input) error {
	bad := func() error { return failure(in, "invalid_request", 0, false) }
	operation := in.Kind + " " + in.Action
	if !contains(operation, "alert-policies list", "alert-policies get", "alert-policies create", "alert-policies update", "alert-policies enable", "alert-policies disable", "alert-policies retry", "alert-policies ranges", "alerts list", "alerts get", "alerts evidence", "alerts series", "alerts transitions", "alerts acknowledge", "alerts processing", "alerts command-result") {
		return bad()
	}
	if operation == "alerts series" {
		if !ascii(in.ConditionID, 1, 64, true) || strings.TrimSpace(in.ConditionID) == "" || in.Limit != 0 && (in.Limit < 2 || in.Limit > 120) {
			return bad()
		}
		var bounds [2]time.Time
		for i, v := range []string{in.Start, in.End} {
			if v != "" {
				value, err := time.Parse(time.RFC3339Nano, v)
				if err != nil || !strings.HasSuffix(v, "Z") || value.IsZero() || value.Year() < 1 || value.Year() > 9999 {
					return bad()
				}
				bounds[i] = value
			}
		}
		if !bounds[0].IsZero() && !bounds[1].IsZero() && (bounds[0].After(bounds[1]) || bounds[1].Sub(bounds[0]) > 24*time.Hour) {
			return bad()
		}
	} else if in.ConditionID != "" || in.Start != "" || in.End != "" {
		return bad()
	}
	if in.Evidence != "" || in.MinimumDuration != "" {
		if operation != "alerts list" || !contains(in.Evidence, "", "matching", "unavailable", "waiting") || in.MinimumDuration != "" && !counter(in.MinimumDuration, false) {
			return bad()
		}
	}
	if in.Action != "list" && in.Action != "create" && in.Action != "command-result" && !validID(in.ID) {
		return bad()
	}
	if in.SLOID != "" && !validID(in.SLOID) || in.ServiceID != "" && !validID(in.ServiceID) {
		return bad()
	}
	if in.Limit < 0 || in.Limit > 200 || !ascii(in.Cursor, 0, 1024, false) || !utf8.ValidString(in.Query) || utf8.RuneCountInString(in.Query) > 256 {
		return bad()
	}
	for _, r := range in.Query {
		if unicode.IsControl(r) {
			return bad()
		}
	}
	if !contains(in.Scope, "", "active", "all", "history") || !contains(in.GroupBy, "", "none", "slo") || !contains(in.Sort, "", "newest", "severity") || !csv(in.State, "firing", "acknowledged", "resolved", "disabled", "superseded") || !csv(in.Severity, "low", "medium", "high") {
		return bad()
	}
	if in.Kind == "alert-policies" && strings.Contains(in.Severity, ",") {
		return bad()
	}
	if in.Action == "ranges" && !counter(in.Revision, true) || !contains(in.Selection, "", "firing", "latest") {
		return bad()
	}
	if in.Action == "command-result" && (!contains(in.Operation, "alert-policy.create", "alert-policy.update", "alert-policy.enable", "alert-policy.disable", "alert-policy.retry", "alert.acknowledge") || !ascii(in.Key, 1, 128, false)) {
		return bad()
	}
	if in.Mutation() {
		if !ascii(in.Key, 1, 128, false) {
			return bad()
		}
		kind := "alert-policy"
		if in.Action == "acknowledge" {
			kind = "alert-occurrence"
		}
		if in.Action != "create" && !ValidETag(in.ETag, kind, in.ID) || in.Action == "create" && in.ETag != "" {
			return bad()
		}
	} else if len(in.Body) > 0 || in.ETag != "" || in.Key != "" && in.Action != "command-result" {
		return bad()
	}
	return nil
}
func validBody(in Input) bool {
	data := bytes.TrimSpace(in.Body)
	if len(data) == 0 && in.Action != "create" && in.Action != "update" {
		data = []byte(`{}`)
	}
	if len(in.Body) > MaxInputBytes || len(data) == 0 || data[0] != '{' || !noNull(data) {
		return false
	}
	decode := func(v any) bool { return json.Unmarshal(data, v, json.RejectUnknownMembers(true)) == nil }
	switch in.Action {
	case "create":
		var v api.AlertPolicyWrite
		return decode(&v) && validName(v.Name) && validID(v.SloId.String()) && validConditions(v.Conditions) && (v.Severity == nil || contains(string(*v.Severity), "low", "medium", "high"))
	case "update":
		var v api.AlertPolicyPatch
		return decode(&v) && (v.Name != nil || v.Severity != nil || v.SloId != nil || v.Conditions != nil) && (v.Name == nil || validName(*v.Name)) && (v.SloId == nil || validID(v.SloId.String())) && (v.Severity == nil || contains(string(*v.Severity), "low", "medium", "high")) && (v.Conditions == nil || validConditions(*v.Conditions)) && (v.Reason == nil || validName(*v.Reason))
	case "acknowledge", "retry":
		var v api.AlertAcknowledge
		return decode(&v)
	default:
		var v api.AlertPolicyCommand
		return decode(&v) && (v.Reason == nil || validName(*v.Reason))
	}
}
func noNull(data []byte) bool {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return false
	}
	switch data[0] {
	case '{':
		var v map[string]jsontext.Value
		if json.Unmarshal(data, &v) != nil {
			return false
		}
		for key, field := range v {
			if key == "slo_id" {
				var id string
				if json.Unmarshal(field, &id) != nil || !validID(id) {
					return false
				}
			}
			if !noNull(field) {
				return false
			}
		}
	case '[':
		var v []jsontext.Value
		if json.Unmarshal(data, &v) != nil {
			return false
		}
		for _, field := range v {
			if !noNull(field) {
				return false
			}
		}
	}
	return true
}
