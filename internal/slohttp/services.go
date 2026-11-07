package slohttp

import (
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
)

func (h *Handler) listCatalogServices(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, query url.Values) {
	if hasBody(r) || hasConditional(r) {
		problem(w, 400, "invalid_request", requestID)
		return
	}
	input := storage.ServiceCatalogInput{Runtime: h.runtime, Proof: proof, Now: h.now}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			problem(w, 400, "invalid_request", requestID)
			return
		}
		switch key {
		case "q":
			input.Query = values[0]
		case "owner":
			input.Owner = values[0]
		case "coverage":
			input.Coverage = values[0]
		case "cursor":
			input.Cursor = values[0]
		case "limit":
			limit, err := strconv.Atoi(values[0])
			if err != nil || limit < 1 || limit > 200 {
				problem(w, 400, "invalid_request", requestID)
				return
			}
			input.Limit = limit
		default:
			problem(w, 400, "invalid_request", requestID)
			return
		}
	}
	page, err := storage.ListServiceCatalog(r.Context(), h.databaseURL, input)
	if err != nil {
		storageError(w, err, requestID)
		return
	}
	items := make([]api.Service, 0, len(page.Services))
	for _, service := range page.Services {
		items = append(items, serviceDTO(service))
	}
	write(w, 200, api.ServicePage{Items: items, NextCursor: page.NextCursor, Total: new(page.Total), AllCount: new(page.All), WithSlosCount: new(page.WithSlos), Owners: &page.Owners})
}
func servicePatch(request api.ServiceMetadataWrite) storage.ServicePatch {
	patch := storage.ServicePatch{Name: request.Name, DisplayName: request.DisplayName, Description: request.Description, Owner: request.Owner, Contact: request.Contact, Labels: request.Labels, Annotations: request.Annotations}
	if request.Links != nil {
		links := make([]storage.ServiceLink, 0, len(*request.Links))
		for _, link := range *request.Links {
			links = append(links, storage.ServiceLink{Name: link.Name, URL: link.Url})
		}
		patch.Links = &links
	}
	if request.Dependencies != nil {
		ids := make([]string, 0, len(*request.Dependencies))
		for _, id := range *request.Dependencies {
			ids = append(ids, id.String())
		}
		patch.Dependencies = &ids
	}
	return patch
}
func serviceDTO(v storage.ServiceRecord) api.Service {
	if v.Labels == nil {
		v.Labels = map[string][]string{}
	}
	if v.Annotations == nil {
		v.Annotations = map[string]string{}
	}
	links := make([]api.ServiceLink, 0, len(v.Links))
	for _, link := range v.Links {
		links = append(links, api.ServiceLink{Name: link.Name, Url: link.URL})
	}
	ids := make([]uuid.UUID, 0, len(v.Dependencies))
	for _, id := range v.Dependencies {
		ids = append(ids, uuid.MustParse(id))
	}
	references := func(records []storage.ServiceReference) []api.ServiceReference {
		values := make([]api.ServiceReference, 0, len(records))
		for _, record := range records {
			values = append(values, api.ServiceReference{Id: uuid.MustParse(record.ID), Name: record.Name, DisplayName: record.DisplayName, Owner: record.Owner})
		}
		return values
	}
	dependencies, usedBy := references(v.DependencyServices), references(v.UsedBy)
	state := v.SloSummary.State
	if state == "" {
		state = "no_slos"
	}
	summary := api.ServiceSloSummary{State: api.ServiceSloSummaryState(state), Total: v.SloSummary.Total, OnTrack: v.SloSummary.OnTrack, Breached: v.SloSummary.Breached, Unknown: v.SloSummary.Unknown}
	return api.Service{Id: uuid.MustParse(v.ID), Name: v.Name, DisplayName: v.DisplayName, Version: strconv.FormatInt(v.Version, 10), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Description: new(v.Description), Owner: new(v.Owner), Contact: new(v.Contact), Labels: &v.Labels, Links: &links, Annotations: &v.Annotations, Dependencies: &ids, DependencyServices: &dependencies, UsedBy: &usedBy, UsedByCount: new(v.UsedByCount), SloSummary: &summary}
}
func setCreateServiceMetadata(input *storage.CreateObjectiveInput, request api.ServiceCreateRequest, body []byte) error {
	if request.Description != nil || request.Owner != nil || request.Contact != nil || request.Labels != nil || request.Links != nil || request.Annotations != nil {
		var metadata storage.ServiceMetadata
		if err := json.Unmarshal(body, &metadata); err != nil {
			return err
		}
		input.ServiceMetadata = &metadata
	}
	if request.Dependencies != nil {
		for _, id := range *request.Dependencies {
			input.Dependencies = append(input.Dependencies, id.String())
		}
	}
	return nil
}
