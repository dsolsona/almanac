package slihttp

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"time"

	"github.com/dsolsona/almanac/internal/storage"
)

func (h *Handler) backfillJobs(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, parts []string) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet && !hasBody(r):
		jobs, err := storage.ListBackfills(r.Context(), h.databaseURL, proof, parts[0], h.now)
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		h.writeJSON(w, http.StatusOK, jobs)
	case len(parts) == 2 && r.Method == http.MethodPost:
		if !singlePrintableHeader(r.Header, "Idempotency-Key", 1, 128) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		body, status := readBody(w, r, managementBodyLimit)
		if status != 0 {
			h.bodyError(w, status, requestID)
			return
		}
		var input struct {
			Start                 time.Time `json:"start"`
			End                   time.Time `json:"end"`
			Preview               *bool     `json:"preview"`
			RetryExhausted        bool      `json:"retry_exhausted"`
			IncludeEarlierHistory bool      `json:"include_earlier_history"`
			EarlierHistoryContext string    `json:"earlier_history_context"`
		}
		if json.Unmarshal(body, &input, json.RejectUnknownMembers(true)) != nil || input.Preview == nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		admitted, err := storage.RequestBackfill(r.Context(), h.databaseURL, storage.BackfillRequest{
			Proof: proof, SLIID: parts[0], Start: input.Start, End: input.End,
			Preview: *input.Preview, RetryExhausted: input.RetryExhausted, IncludeEarlierHistory: input.IncludeEarlierHistory, EarlierHistoryContext: input.EarlierHistoryContext, RequestKey: r.Header.Get("Idempotency-Key"), Now: h.now,
		})
		switch {
		case errors.Is(err, storage.ErrBackfillEarlierUnavailable):
			h.writeProblem(w, http.StatusUnprocessableEntity, "backfill_earlier_unavailable", requestID)
		case errors.Is(err, storage.ErrBackfillEarlierChanged):
			h.writeProblem(w, http.StatusConflict, "backfill_earlier_changed", requestID)
		case errors.Is(err, storage.ErrBackfillEarlierRange):
			h.writeProblem(w, http.StatusUnprocessableEntity, "backfill_earlier_range", requestID)
		case errors.Is(err, storage.ErrBackfillNoGaps):
			h.writeProblem(w, http.StatusConflict, "backfill_no_gaps", requestID)
		case errors.Is(err, storage.ErrBackfillRange):
			h.writeProblem(w, http.StatusUnprocessableEntity, "backfill_range", requestID)
		case errors.Is(err, storage.ErrBackfillOverlap):
			h.writeProblem(w, http.StatusConflict, "backfill_overlap", requestID)
		case err != nil:
			h.writeStorageError(w, err, requestID)
		default:
			status := http.StatusAccepted
			if *input.Preview {
				status = http.StatusOK
			}
			h.writeJSON(w, status, admitted)
		}
	case len(parts) == 3 && canonicalID(parts[2]) && r.Method == http.MethodDelete && !hasBody(r):
		if err := storage.CancelBackfill(r.Context(), h.databaseURL, proof, parts[0], parts[2], h.now); err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
	}
}
