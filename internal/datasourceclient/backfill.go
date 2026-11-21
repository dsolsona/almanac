package datasourceclient

import (
	"context"
	"encoding/json/v2"
	"net/http"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/oapi-codegen/runtime/types"
)

type backfillOperations interface {
	ListSliBackfills(context.Context, api.SliId, ...api.RequestEditorFn) (*http.Response, error)
	RequestSliBackfill(context.Context, api.SliId, *api.RequestSliBackfillParams, api.RequestSliBackfillJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	CancelSliBackfill(context.Context, api.SliId, types.UUID, ...api.RequestEditorFn) (*http.Response, error)
}

func (c *Client) Backfill(ctx context.Context, action, sliID, jobID, key string, input api.SliBackfillRequest) ([]byte, error) {
	id, err := parseSliID(sliID)
	if err != nil {
		return nil, err
	}
	ops, ok := c.ops.(backfillOperations)
	if !ok {
		return nil, protocol()
	}
	switch action {
	case "list":
		response, err := ops.ListSliBackfills(ctx, id, c.editor)
		return c.read(response, err, http.StatusOK, func(body []byte) bool {
			var jobs []api.SliBackfillJob
			if json.Unmarshal(body, &jobs) != nil || jobs == nil || len(jobs) > 20 {
				return false
			}
			for _, job := range jobs {
				if !uuidPattern.MatchString(uuidText(job.Id)) || !job.State.Valid() || !job.Start.Before(job.End) ||
					job.Chunks < 1 || job.Committed < 0 || job.Gaps < 0 || job.Committed+job.Gaps > job.Chunks {
					return false
				}
			}
			return true
		})
	case "preview", "request":
		if !validKey(key) || !input.Start.Before(input.End) {
			return nil, invalid()
		}
		input.Preview = action == "preview"
		status := http.StatusAccepted
		if input.Preview {
			status = http.StatusOK
		}
		response, err := ops.RequestSliBackfill(ctx, id, &api.RequestSliBackfillParams{IdempotencyKey: key}, input, c.mutationEditor(key, ""))
		return c.read(response, err, status, func(body []byte) bool {
			var result api.SliBackfillAdmission
			return json.Unmarshal(body, &result) == nil && result.Start.Equal(input.Start) && result.End.Equal(input.End) &&
				result.ChunkCount > 0 && (input.Preview || result.Id != nil && uuidPattern.MatchString(uuidText(*result.Id)))
		})
	case "cancel":
		job, err := parseID(jobID)
		if err != nil {
			return nil, err
		}
		response, err := ops.CancelSliBackfill(ctx, id, job, c.editor)
		if _, err := c.read(response, err, http.StatusNoContent, func(body []byte) bool { return len(body) == 0 }); err != nil {
			return nil, err
		}
		return []byte(`{"cancelled":true}`), nil
	default:
		return nil, invalid()
	}
}
