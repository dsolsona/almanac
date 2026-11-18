package slohttp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"

	"github.com/google/uuid"
)

func decodeScopedMaintenanceWrite(body []byte, input *storage.CreateMaintenanceInput) bool {
	var req struct {
		Title  string         `json:"title"`
		Reason string         `json:"reason"`
		Start  time.Time      `json:"start"`
		End    time.Time      `json:"end"`
		Scope  jsontext.Value `json:"scope"`
	}
	if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
		return false
	}
	selector, valid := api.ParseMaintenanceScope(req.Scope)
	if !valid {
		return false
	}
	input.Title, input.Reason, input.Start, input.End = req.Title, req.Reason, req.Start, req.End
	input.ScopeKind = storage.MaintenanceScopeKind(selector.Kind)
	switch selector.Kind {
	case "slos":
		input.SloIDs = selector.IDs
	case "services":
		input.ServiceIDs = selector.IDs
	}
	return true
}

func (h *Handler) previewMaintenance(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, query url.Values) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	limit, cursor, _, valid := pageQuery(query, false)
	if !valid || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	body, status := readBody(w, r)
	if status != 0 {
		problem(w, status, "invalid_request", requestID)
		return
	}
	var req struct {
		Scope jsontext.Value `json:"scope"`
	}
	if json.Unmarshal(body, &req, json.RejectUnknownMembers(true)) != nil {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	selector, valid := api.ParseMaintenanceScope(req.Scope)
	if !valid {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	input := storage.MaintenanceScopePreviewInput{Proof: proof, ScopeKind: storage.MaintenanceScopeKind(selector.Kind), Limit: limit, Cursor: cursor, Now: h.now}
	switch selector.Kind {
	case "slos":
		input.SloIDs = selector.IDs
	case "services":
		input.ServiceIDs = selector.IDs
	}
	page, err := storage.PreviewMaintenanceScope(r.Context(), h.databaseURL, input)
	writeMaintenanceImpact(w, page, err, requestID)
}

func (h *Handler) maintenanceImpact(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, id string, query url.Values) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", requestID)
		return
	}
	revision, validRevision := maintenanceRevisionQuery(query)
	limit, cursor, _, validPage := pageQuery(query, false)
	if !validPage || !validRevision || hasConditional(r) || hasBody(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	page, err := storage.ReadMaintenanceImpact(r.Context(), h.databaseURL, storage.MaintenanceImpactInput{
		Proof: proof, ID: id, Revision: revision, Limit: limit, Cursor: cursor, Now: h.now,
	})
	writeMaintenanceImpact(w, page, err, requestID)
}

func writeMaintenanceImpact(w http.ResponseWriter, page storage.MaintenanceImpactPage, err error, requestID string) {
	if err != nil {
		maintenanceError(w, err, requestID)
		return
	}
	out := api.MaintenanceImpactPage{InventoryAt: page.InventoryAt, Total: strconv.FormatInt(page.Total, 10), Ready: page.Ready,
		Items: make([]api.MaintenanceImpactMember, 0, len(page.Items))}
	for _, member := range page.Items {
		item := api.MaintenanceImpactMember{SloId: uuid.MustParse(member.SloID), SloName: member.SloName,
			ServiceId: uuid.MustParse(member.ServiceID), ServiceName: member.ServiceName}
		if !member.Start.IsZero() {
			item.Start, item.End = new(member.Start), new(member.End)
		}
		out.Items = append(out.Items, item)
	}
	if page.NextCursor != "" {
		out.NextCursor = new(page.NextCursor)
	}
	write(w, 200, out)
}

func scopedMaintenanceDTO(record storage.MaintenanceRecord) api.ScopedMaintenanceWindow {
	legacy := maintenanceDTO(record)
	var scope api.MaintenanceScope
	switch string(record.ScopeKind) {
	case "services":
		ids := make([]uuid.UUID, len(record.ServiceIDs))
		for i, id := range record.ServiceIDs {
			ids[i] = uuid.MustParse(id)
		}
		_ = scope.FromMaintenanceServiceScope(api.MaintenanceServiceScope{Kind: "services", ServiceIds: ids})
	case "all_services":
		_ = scope.FromMaintenanceAllServicesScope(api.MaintenanceAllServicesScope{Kind: "all_services"})
	default:
		_ = scope.FromMaintenanceSloScope(api.MaintenanceSloScope{Kind: "slos", SloIds: legacy.SloIds})
	}
	return api.ScopedMaintenanceWindow{
		Id: legacy.Id, Title: legacy.Title, Reason: legacy.Reason, Start: legacy.Start, End: legacy.End,
		Revision: legacy.Revision, Scope: scope, Operation: api.ScopedMaintenanceWindowOperation(legacy.Operation),
		CreatedAt: legacy.CreatedAt, UpdatedAt: legacy.UpdatedAt, CreatorId: legacy.CreatorId,
		ActorId: legacy.ActorId, AuditId: legacy.AuditId, Tombstone: legacy.Tombstone,
	}
}

func writeScopedMaintenancePage(w http.ResponseWriter, page storage.MaintenancePage, err error, requestID string) {
	if err != nil {
		maintenanceError(w, err, requestID)
		return
	}
	out := api.ScopedMaintenancePage{Items: make([]api.ScopedMaintenanceWindow, 0, len(page.Items))}
	for _, record := range page.Items {
		out.Items = append(out.Items, scopedMaintenanceDTO(record))
	}
	if page.NextCursor != "" {
		out.NextCursor = new(page.NextCursor)
	}
	write(w, 200, out)
}

func writeScopedMaintenanceTimeline(w http.ResponseWriter, page storage.MaintenancePage, err error, requestID string) {
	if err != nil {
		maintenanceError(w, err, requestID)
		return
	}
	out := api.ScopedMaintenanceTimelinePage{Pending: page.Pending, Items: make([]api.ScopedMaintenanceTimelineInterval, 0, len(page.Items))}
	for _, record := range page.Items {
		out.Items = append(out.Items, api.ScopedMaintenanceTimelineInterval{
			Window: scopedMaintenanceDTO(record), Start: record.EffectiveStart, End: record.EffectiveEnd,
		})
	}
	if page.Overflow != nil {
		out.Overflow = &api.ScopedMaintenanceOverflow{Start: page.Overflow.Start, End: page.Overflow.End, Count: strconv.FormatInt(page.Overflow.Count, 10)}
	}
	if page.NextCursor != "" {
		out.NextCursor = new(page.NextCursor)
	}
	write(w, 200, out)
}
