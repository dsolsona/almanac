package slohttp

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
)

const maintenancePath = "/api/v1/maintenance"
const scopedMaintenancePath = "/api/v1/scoped-maintenance"

func (h *Handler) maintenance(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, parts []string, query url.Values) {
	scoped := parts[0] == "scoped-maintenance"
	if scoped && len(parts) == 2 && parts[1] == "preview" {
		h.previewMaintenance(w, r, requestID, proof, query)
		return
	}
	id, action, method := "", "", r.Method
	allow := "GET, POST"
	switch len(parts) {
	case 1:
		if method == http.MethodPost {
			action = "create"
		}
	case 2, 3:
		id = parts[1]
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			problem(w, 404, "not_found", requestID)
			return
		}
		if scoped && len(parts) == 3 && parts[2] == "impact" {
			h.maintenanceImpact(w, r, requestID, proof, id, query)
			return
		}
		allow = "GET, PUT"
		if method == http.MethodPut {
			action = "edit"
		}
		if len(parts) == 3 {
			allow = "POST"
			switch parts[2] {
			case "end", "cancel", "withdraw":
				action = parts[2]
			default:
				problem(w, 404, "not_found", requestID)
				return
			}
		}
	default:
		problem(w, 404, "not_found", requestID)
		return
	}
	if len(parts) < 3 && method == http.MethodGet {
		h.readMaintenance(w, r, requestID, proof, id, query, scoped)
		return
	}
	if action == "" || (len(parts) == 3 && method != http.MethodPost) {
		w.Header().Set("Allow", allow)
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if len(query) != 0 || !printableHeader(r.Header, "Idempotency-Key") || len(r.Header.Values("If-None-Match")) != 0 {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	revision := int64(0)
	if id == "" {
		if len(r.Header.Values("If-Match")) != 0 {
			problem(w, 400, "invalid_request", requestID)
			return
		}
	} else {
		var valid bool
		revision, valid = parseETag(r.Header.Values("If-Match"), "maintenance", id)
		if !valid {
			problem(w, 400, "invalid_request", requestID)
			return
		}
	}
	body, status := readBody(w, r)
	if status != 0 {
		problem(w, status, "invalid_request", requestID)
		return
	}
	command := storage.MaintenanceCommandInput{Proof: proof, ID: id, Revision: revision,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), RequestID: requestID, Now: h.now, Scoped: scoped}
	ctx := r.Context()
	var record storage.MaintenanceRecord
	var err error
	switch action {
	case "create", "edit":
		input := storage.CreateMaintenanceInput{Proof: proof, ID: id, Revision: revision,
			IdempotencyKey: command.IdempotencyKey, RequestID: requestID, Now: h.now, Scoped: scoped}
		if scoped {
			if !decodeScopedMaintenanceWrite(body, &input) {
				problem(w, 400, "invalid_request", requestID)
				return
			}
		} else {
			var legacy struct {
				Title  string    `json:"title"`
				Reason string    `json:"reason"`
				Start  time.Time `json:"start"`
				End    time.Time `json:"end"`
				SloIDs []string  `json:"slo_ids"`
			}
			if json.Unmarshal(body, &legacy, json.RejectUnknownMembers(true)) != nil {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Title, input.Reason = legacy.Title, legacy.Reason
			input.Start, input.End, input.SloIDs = legacy.Start, legacy.End, legacy.SloIDs
		}
		if action == "create" {
			record, err = storage.CreateMaintenance(ctx, h.databaseURL, input)
		} else {
			record, err = storage.EditMaintenance(ctx, h.databaseURL, input)
		}
	case "end":
		var req api.MaintenanceEnd
		if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		record, err = storage.EndMaintenance(ctx, h.databaseURL, storage.EndMaintenanceInput{
			Proof: proof, ID: id, Revision: revision, IdempotencyKey: command.IdempotencyKey,
			RequestID: requestID, Now: h.now, End: req.End, Reason: req.Reason, Scoped: scoped,
		})
	case "cancel", "withdraw":
		var req api.MaintenanceReason
		if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		command.Reason = req.Reason
		if action == "cancel" {
			record, err = storage.CancelMaintenance(ctx, h.databaseURL, command)
		} else {
			record, err = storage.WithdrawMaintenance(ctx, h.databaseURL, command)
		}
	}
	if err != nil {
		maintenanceWriteError(w, err, requestID)
		return
	}
	status = 200
	if action == "create" {
		status = 201
		path := maintenancePath
		if scoped {
			path = scopedMaintenancePath
		}
		w.Header().Set("Location", path+"/"+record.ID)
	}
	writeMaintenance(w, status, record, scoped)
}

func (h *Handler) readMaintenance(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, query url.Values, scoped bool) {
	if hasBody(r) || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	if id == "" {
		limit, cursor, _, ok := pageQuery(query, false)
		if !ok {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		page, err := storage.ListMaintenance(r.Context(), h.databaseURL, storage.ListMaintenanceInput{Proof: proof, Limit: limit, Cursor: cursor, Now: h.now, Scoped: scoped})
		if scoped {
			writeScopedMaintenancePage(w, page, err, requestID)
		} else {
			writeMaintenancePage(w, page, err, requestID)
		}
		return
	}
	revision, valid := maintenanceRevisionQuery(query)
	if !valid || len(query) != 0 {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	record, err := storage.ReadMaintenance(r.Context(), h.databaseURL, storage.ReadMaintenanceInput{Proof: proof, ID: id, Revision: revision, Now: h.now, Scoped: scoped})
	if err != nil {
		maintenanceError(w, err, requestID)
		return
	}
	writeMaintenance(w, 200, record, scoped)
}

func (h *Handler) sloMaintenance(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, query url.Values, scoped bool) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	if hasBody(r) || hasConditional(r) || len(query["start"]) != 1 || len(query["end"]) != 1 {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	start, startErr := time.Parse(time.RFC3339Nano, query.Get("start"))
	end, endErr := time.Parse(time.RFC3339Nano, query.Get("end"))
	query.Del("start")
	query.Del("end")
	overflowValues, includeOverflow := query["include_overflow"]
	if includeOverflow && (len(overflowValues) != 1 || overflowValues[0] != "true" && overflowValues[0] != "false") {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	includeOverflow = includeOverflow && overflowValues[0] == "true"
	query.Del("include_overflow")
	limit, cursor, _, ok := pageQuery(query, false)
	if !ok || startErr != nil || endErr != nil {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	page, err := storage.ListSloMaintenance(r.Context(), h.databaseURL, storage.ListSloMaintenanceInput{
		ListMaintenanceInput: storage.ListMaintenanceInput{Proof: proof, Limit: limit, Cursor: cursor, Now: h.now, Scoped: scoped},
		SloID:                id, Start: start, End: end,
		IncludeOverflow: includeOverflow,
	})
	if scoped {
		writeScopedMaintenanceTimeline(w, page, err, requestID)
	} else {
		writeMaintenancePage(w, page, err, requestID)
	}
}

func maintenanceRevisionQuery(query url.Values) (int64, bool) {
	values, present := query["revision"]
	if !present {
		return 0, true
	}
	query.Del("revision")
	if len(values) != 1 {
		return 0, false
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	return value, err == nil && value > 0 && strconv.FormatInt(value, 10) == values[0]
}

func maintenanceDTO(record storage.MaintenanceRecord) api.MaintenanceWindow {
	slos := make([]uuid.UUID, len(record.SloIDs))
	for i, id := range record.SloIDs {
		slos[i] = uuid.MustParse(id)
	}
	return api.MaintenanceWindow{
		Id: uuid.MustParse(record.ID), Title: record.Title, Reason: record.Reason, Start: record.Start, End: record.End,
		Revision: strconv.FormatInt(record.Revision, 10), SloIds: slos, Operation: api.MaintenanceWindowOperation(record.Operation),
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, CreatorId: uuid.MustParse(record.CreatorID),
		ActorId: uuid.MustParse(record.ActorID), AuditId: uuid.MustParse(record.AuditID), Tombstone: record.Tombstone,
	}
}

func writeMaintenance(w http.ResponseWriter, status int, record storage.MaintenanceRecord, scoped bool) {
	w.Header().Set("ETag", etag("maintenance", record.ID, record.Revision))
	if scoped {
		write(w, status, scopedMaintenanceDTO(record))
	} else {
		write(w, status, maintenanceDTO(record))
	}
}

func writeMaintenancePage(w http.ResponseWriter, page storage.MaintenancePage, err error, requestID string) {
	if err != nil {
		maintenanceError(w, err, requestID)
		return
	}
	out := api.MaintenancePage{Items: make([]api.MaintenanceWindow, 0, len(page.Items))}
	if page.Overflow != nil {
		out.Overflow = &api.MaintenanceOverflow{
			Start: page.Overflow.Start, End: page.Overflow.End, Count: strconv.FormatInt(page.Overflow.Count, 10),
		}
	}
	for _, record := range page.Items {
		out.Items = append(out.Items, maintenanceDTO(record))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	write(w, 200, out)
}

func maintenanceWriteError(w http.ResponseWriter, err error, requestID string) {
	if errors.Is(err, storage.ErrMaintenanceSnapshotChanged) {
		writeMaintenanceProblem(w, http.StatusConflict, api.Problem{
			Code: "maintenance_write_retry", Message: "The affected SLO inventory changed or the scheduled start passed during validation. Retry the same request with the same idempotency key.",
			RequestId: requestID,
		})
		return
	}
	maintenanceError(w, err, requestID)
}

func maintenanceError(w http.ResponseWriter, err error, requestID string) {
	p := api.Problem{RequestId: requestID}
	var status int
	switch {
	case errors.Is(err, storage.ErrMaintenanceConflict):
		status, p.Code, p.Message = 409, "maintenance_conflict", "The window changed or the request key was already used."
		if conflict, ok := errors.AsType[*storage.MaintenanceConflictError](err); ok {
			p.CurrentRevision = new(strconv.FormatInt(conflict.CurrentRevision, 10))
		}
	case errors.Is(err, storage.ErrMaintenanceSnapshotChanged):
		status, p.Code, p.Message = 409, "maintenance_snapshot_changed", "The maintenance inventory changed. Reload the first page."
	case errors.Is(err, storage.ErrMaintenanceAlignment):
		status, p.Code, p.Message = 400, "maintenance_alignment", "Choose bounds aligned with the affected SLO observation grids."
		if alignment, ok := errors.AsType[*storage.MaintenanceAlignmentError](err); ok {
			ids := make([]uuid.UUID, len(alignment.SloIDs))
			for i, id := range alignment.SloIDs {
				ids[i] = uuid.MustParse(id)
			}
			p.BlockingSloIds = &ids
			if !alignment.SuggestedStart.IsZero() && !alignment.SuggestedEnd.IsZero() {
				p.SuggestedStart, p.SuggestedEnd = &alignment.SuggestedStart, &alignment.SuggestedEnd
			}
		}
	case errors.Is(err, storage.ErrMaintenanceNotFound):
		status, p.Code, p.Message = 404, "not_found", "Maintenance resource not found."
	case errors.Is(err, storage.ErrMaintenanceInvalid):
		status, p.Code, p.Message = 400, "invalid_request", "Invalid maintenance request."
	default:
		storageError(w, err, requestID)
		return
	}
	writeMaintenanceProblem(w, status, p)
}

func writeMaintenanceProblem(w http.ResponseWriter, status int, p api.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, p)
}
