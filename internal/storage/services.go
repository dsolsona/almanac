package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ServiceLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type ServiceMetadata struct {
	Description string              `json:"description"`
	Owner       string              `json:"owner"`
	Contact     string              `json:"contact"`
	Labels      map[string][]string `json:"labels"`
	Links       []ServiceLink       `json:"links"`
	Annotations map[string]string   `json:"annotations"`
}
type ServiceReference struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Owner       string `json:"owner"`
}
type ServiceSloSummary struct {
	State    string `json:"state"`
	Total    int    `json:"total"`
	OnTrack  int    `json:"on_track"`
	Breached int    `json:"breached"`
	Unknown  int    `json:"unknown"`
}
type ServicePatch struct {
	DisplayName                       string
	Name, Description, Owner, Contact *string
	Labels                            *map[string][]string
	Links                             *[]ServiceLink
	Annotations                       *map[string]string
	Dependencies                      *[]string
}
type ServiceUpdateInput struct {
	Proof     DatasourceProof
	ID        string
	Patch     ServicePatch
	Version   int64
	RequestID string
	Now       func() time.Time
}

var ErrServiceReferenced = errors.New("storage: service is still referenced")

type ServiceReferencesError struct{ Slos, Services []string }

func (e *ServiceReferencesError) Error() string { return ErrServiceReferenced.Error() }
func (e *ServiceReferencesError) Unwrap() error { return ErrServiceReferenced }

var serviceLabelKey = regexp.MustCompile(`^[A-Za-z0-9]([-._A-Za-z0-9]{0,61}[A-Za-z0-9])?$`)

func serviceText(value string, limit int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}
func validServiceMetadata(value ServiceMetadata) bool {
	if !serviceText(value.Description, 4000, true) || !serviceText(value.Owner, 200, false) || !serviceText(value.Contact, 320, false) || len(value.Links) > 50 || len(value.Annotations) > 100 {
		return false
	}
	pairs := 0
	for key, values := range value.Labels {
		if !serviceLabelKey.MatchString(key) || len(values) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, v := range values {
			pairs++
			if pairs > 100 || strings.TrimSpace(v) == "" || !serviceText(v, 256, false) || seen[v] {
				return false
			}
			seen[v] = true
		}
	}
	seen := map[ServiceLink]bool{}
	for _, link := range value.Links {
		if strings.TrimSpace(link.Name) == "" || !serviceText(link.Name, 80, false) || !serviceText(link.URL, 2048, false) || strings.ContainsAny(link.URL, "\r\n\t ") || seen[link] {
			return false
		}
		parsed, err := url.ParseRequestURI(link.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			return false
		}
		seen[link] = true
	}
	for key, v := range value.Annotations {
		if !serviceLabelKey.MatchString(key) || !serviceText(v, 4096, true) {
			return false
		}
	}
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= 64*1024
}
func normalizeServiceMetadata(value ServiceMetadata) ServiceMetadata {
	if value.Labels == nil {
		value.Labels = map[string][]string{}
	}
	if value.Links == nil {
		value.Links = []ServiceLink{}
	}
	if value.Annotations == nil {
		value.Annotations = map[string]string{}
	}
	return value
}
func lockServiceCatalog(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('almanac.service.catalog',0))")
	return err
}
func writeServiceDependencies(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, dependencies []string) error {
	if len(dependencies) > 100 {
		return ErrObjectiveInvalid
	}
	ids := make([]pgtype.UUID, 0, len(dependencies))
	seen := map[string]bool{}
	for _, text := range dependencies {
		target, valid := canonicalUUID(text)
		if !valid || target == id || seen[text] {
			return ErrObjectiveInvalid
		}
		seen[text] = true
		ids = append(ids, target)
	}
	count, err := q.CountActiveServiceIDs(ctx, ids)
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrObjectiveNotFound
	}
	if err = q.ReplaceServiceDependenciesDelete(ctx, id); err != nil {
		return err
	}
	return q.ReplaceServiceDependenciesInsert(ctx, dbgen.ReplaceServiceDependenciesInsertParams{ServiceID: id, Column2: ids})
}
func enrichServices(ctx context.Context, q *dbgen.Queries, services []ServiceRecord, now time.Time) error {
	if len(services) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(services))
	positions := map[string]int{}
	for i, service := range services {
		id, _ := canonicalUUID(service.ID)
		ids = append(ids, id)
		positions[service.ID] = i
		services[i].SloSummary = ServiceSloSummary{State: "no_slos"}
	}
	rows, err := q.ReadServiceCatalogFields(ctx, ids)
	if err != nil {
		return err
	}
	for _, row := range rows {
		service := &services[positions[row.ID.String()]]
		if json.Unmarshal(row.Metadata, &service.ServiceMetadata) != nil || json.Unmarshal(row.Dependencies, &service.DependencyServices) != nil || json.Unmarshal(row.UsedBy, &service.UsedBy) != nil {
			return errors.New("storage: invalid service catalog metadata")
		}
		service.ServiceMetadata = normalizeServiceMetadata(service.ServiceMetadata)
		service.Dependencies = []string{}
		for _, target := range service.DependencyServices {
			service.Dependencies = append(service.Dependencies, target.ID)
		}
		service.UsedByCount = int(row.UsedByCount)
	}
	states, err := q.ListServiceSloStates(ctx, dbgen.ListServiceSloStatesParams{ServiceIds: ids, Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
	if err != nil {
		return err
	}
	for _, state := range states {
		summary := &services[positions[state.ServiceID.String()]].SloSummary
		summary.Total++
		switch state.State {
		case "violated":
			summary.Breached++
		case "met":
			summary.OnTrack++
		default:
			summary.Unknown++
		}
	}
	for i := range services {
		summary := &services[i].SloSummary
		switch {
		case summary.Breached > 0:
			summary.State = "breached"
		case summary.Unknown > 0:
			summary.State = "unknown"
		case summary.Total > 0:
			summary.State = "on_track"
		}
	}
	return nil
}
func enrichedService(ctx context.Context, q *dbgen.Queries, service ServiceRecord, now time.Time) (ServiceRecord, error) {
	records := []ServiceRecord{service}
	err := enrichServices(ctx, q, records, now)
	return records[0], err
}
func UpdateServiceCatalog(ctx context.Context, databaseURL string, input ServiceUpdateInput) (ServiceRecord, error) {
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Version < 1 || input.RequestID == "" || input.Now == nil || !validSliDisplayName(input.Patch.DisplayName) {
		return ServiceRecord{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return ServiceRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if err = lockServiceCatalog(ctx, tx); err != nil {
		return ServiceRecord{}, err
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM public.almanac_services WHERE id=$1 AND archived_at IS NULL FOR UPDATE", id).Scan(&version); errors.Is(err, pgx.ErrNoRows) {
		return ServiceRecord{}, ErrObjectiveNotFound
	} else if err != nil {
		return ServiceRecord{}, err
	}
	if version != input.Version {
		return ServiceRecord{}, ErrObjectiveConflict
	}
	row, err := q.GetService(ctx, id)
	if err != nil {
		return ServiceRecord{}, err
	}
	current, err := enrichedService(ctx, q, serviceRecord(row), normalizedAgentNow(input.Now))
	if err != nil {
		return ServiceRecord{}, err
	}
	name := current.Name
	if input.Patch.Name != nil {
		name = *input.Patch.Name
	}
	if !serviceLabelKey.MatchString(name) || strings.ToLower(name) != name || strings.ContainsAny(name, "._") {
		return ServiceRecord{}, ErrObjectiveInvalid
	}
	metadata := current.ServiceMetadata
	if input.Patch.Description != nil {
		metadata.Description = *input.Patch.Description
	}
	if input.Patch.Owner != nil {
		metadata.Owner = *input.Patch.Owner
	}
	if input.Patch.Contact != nil {
		metadata.Contact = *input.Patch.Contact
	}
	if input.Patch.Labels != nil {
		metadata.Labels = *input.Patch.Labels
	}
	if input.Patch.Links != nil {
		metadata.Links = *input.Patch.Links
	}
	if input.Patch.Annotations != nil {
		metadata.Annotations = *input.Patch.Annotations
	}
	metadata = normalizeServiceMetadata(metadata)
	if !validServiceMetadata(metadata) {
		return ServiceRecord{}, ErrObjectiveInvalid
	}
	raw, err := json.Marshal(metadata, json.Deterministic(true))
	if err != nil {
		return ServiceRecord{}, err
	}
	before, err := json.Marshal(current.ServiceMetadata, json.Deterministic(true))
	if err != nil {
		return ServiceRecord{}, err
	}
	dependencies := slices.Clone(current.Dependencies)
	if input.Patch.Dependencies != nil {
		dependencies = slices.Clone(*input.Patch.Dependencies)
	}
	ordered, old := slices.Clone(dependencies), slices.Clone(current.Dependencies)
	slices.Sort(ordered)
	slices.Sort(old)
	if name != current.Name || input.Patch.DisplayName != current.DisplayName || !bytes.Equal(raw, before) || !slices.Equal(ordered, old) {
		if err = writeServiceDependencies(ctx, q, id, dependencies); err != nil {
			return ServiceRecord{}, err
		}
		row, err = q.UpdateServiceCatalog(ctx, dbgen.UpdateServiceCatalogParams{ID: id, Name: name, DisplayName: input.Patch.DisplayName, Metadata: raw, UpdatedAt: timestamp(normalizedAgentNow(input.Now)), Version: version})
		if isUniqueViolation(err) {
			return ServiceRecord{}, ErrObjectiveConflict
		}
		if err != nil {
			return ServiceRecord{}, err
		}
		if err = q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: id, Action: "service.metadata.updated", OccurredAt: timestamp(normalizedAgentNow(input.Now)), RequestID: input.RequestID}); err != nil {
			return ServiceRecord{}, err
		}
	}
	result, err := enrichedService(ctx, q, serviceRecord(row), normalizedAgentNow(input.Now))
	if err != nil {
		return ServiceRecord{}, err
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return ServiceRecord{}, err
	}
	return result, nil
}
func ArchiveService(ctx context.Context, databaseURL string, proof DatasourceProof, idText string, version int64, requestID string, clock func() time.Time) error {
	id, valid := canonicalUUID(idText)
	if !valid || version < 1 || requestID == "" || clock == nil {
		return ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, clock, "administrator", "editor")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if err = lockServiceCatalog(ctx, tx); err != nil {
		return err
	}
	row, err := q.GetService(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObjectiveNotFound
	} else if err != nil {
		return err
	}
	if row.Version != version {
		return ErrObjectiveConflict
	}
	blockers, err := q.ServiceArchiveBlockers(ctx, id)
	if err != nil {
		return err
	}
	refs := &ServiceReferencesError{}
	if json.Unmarshal(blockers.Slos, &refs.Slos) != nil || json.Unmarshal(blockers.Services, &refs.Services) != nil {
		return errors.New("storage: invalid service references")
	}
	if len(refs.Slos) > 0 || len(refs.Services) > 0 {
		return refs
	}
	affected, err := q.ArchiveService(ctx, dbgen.ArchiveServiceParams{ID: id, ArchivedAt: timestamp(normalizedAgentNow(clock)), Version: version})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrObjectiveConflict
	}
	if err = q.ReplaceServiceDependenciesDelete(ctx, id); err != nil {
		return err
	}
	if err = q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: id, Action: "service.archived", OccurredAt: timestamp(normalizedAgentNow(clock)), RequestID: requestID}); err != nil {
		return err
	}
	return finishAgentRead(ctx, tx, clock, started, user, installation, session)
}

type ServiceCatalogInput struct {
	Runtime                        *Runtime
	Proof                          DatasourceProof
	Query, Owner, Coverage, Cursor string
	Sort, Order                    string
	Limit                          int
	Now                            func() time.Time
}
type ServiceCatalogPage struct {
	Services             []ServiceRecord
	NextCursor           string
	Total, All, WithSlos int
	Owners               []string
}

func ListServiceCatalog(ctx context.Context, databaseURL string, input ServiceCatalogInput) (ServiceCatalogPage, error) {
	if input.Now == nil || !serviceText(input.Query, 128, false) || !serviceText(input.Owner, 200, false) {
		return ServiceCatalogPage{}, ErrObjectiveInvalid
	}
	if input.Coverage == "" {
		input.Coverage = "all"
	}
	if !slices.Contains([]string{"all", "with", "without"}, input.Coverage) {
		return ServiceCatalogPage{}, ErrObjectiveInvalid
	}
	if input.Sort != "" {
		if input.Order == "" {
			input.Order = "asc"
		}
		if !slices.Contains([]string{"name", "slo_status", "owner", "slo_count", "dependency_count"}, input.Sort) || !slices.Contains([]string{"asc", "desc"}, input.Order) {
			return ServiceCatalogPage{}, ErrObjectiveInvalid
		}
	} else if input.Order != "" {
		return ServiceCatalogPage{}, ErrObjectiveInvalid
	}
	filter := ""
	if input.Query != "" || input.Owner != "" || input.Coverage != "all" {
		raw, _ := json.Marshal([]string{input.Query, input.Owner, input.Coverage})
		digest := sha256.Sum256(raw)
		filter = hex.EncodeToString(digest[:])
	}
	if input.Sort != "" {
		raw, _ := json.Marshal([]string{input.Query, input.Owner, input.Coverage, input.Sort, input.Order})
		digest := sha256.Sum256(raw)
		filter = hex.EncodeToString(digest[:])
	}
	legacyCursor := input.Cursor
	var sortedPosition serviceSortCursor
	if input.Sort != "" {
		legacyCursor = ""
		if input.Cursor != "" {
			var err error
			sortedPosition, err = decodeServiceSortCursor(input.Cursor, filter)
			if err != nil {
				return ServiceCatalogPage{}, err
			}
		}
	}
	limit, position, err := validateAgentPage("services", filter, input.Limit, legacyCursor)
	if err != nil {
		return ServiceCatalogPage{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return ServiceCatalogPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	at := normalizedAgentNow(input.Now)
	var rows []dbgen.AlmanacService
	var sortedRows []dbgen.ListSortedCatalogServicesRow
	if input.Sort == "" {
		rows, err = q.ListCatalogServices(ctx, dbgen.ListCatalogServicesParams{AfterTime: timestamp(position.Time), AfterID: uuidOrZero(position.ID), QueryText: input.Query, OwnerName: input.Owner, Coverage: input.Coverage, PageLimit: int32(limit + 1)})
	} else {
		sortedRows, err = q.ListSortedCatalogServices(ctx, dbgen.ListSortedCatalogServicesParams{QueryText: input.Query, OwnerName: input.Owner, Coverage: input.Coverage, SortKey: input.Sort, Descending: input.Order == "desc", HasCursor: input.Cursor != "", AfterNumber: sortedPosition.Number, AfterText: sortedPosition.Text, AfterID: uuidOrZero(sortedPosition.ID), PageLimit: int32(limit + 1), Now: timestamp(at), CalculationVersion: operationalCalculationVersion})
		for _, row := range sortedRows {
			rows = append(rows, row.AlmanacService)
		}
	}
	if err != nil {
		return ServiceCatalogPage{}, err
	}
	page := ServiceCatalogPage{Services: []ServiceRecord{}, Owners: []string{}}
	for _, row := range rows[:min(len(rows), limit)] {
		page.Services = append(page.Services, serviceRecord(row))
	}
	if err = enrichServices(ctx, q, page.Services, at); err != nil {
		return ServiceCatalogPage{}, err
	}
	// Keep metadata-rich pages comfortably inside the existing 4 MiB response bound.
	size := 0
	for i, record := range page.Services {
		raw, e := json.Marshal(record)
		if e != nil {
			return ServiceCatalogPage{}, e
		}
		if i > 0 && size+len(raw) > 2*1024*1024 {
			page.Services = page.Services[:i]
			break
		}
		size += len(raw)
	}
	if len(rows) > len(page.Services) && len(page.Services) > 0 {
		last := page.Services[len(page.Services)-1]
		if input.Sort == "" {
			page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "services", AgentID: filter, Time: last.CreatedAt, ID: last.ID})
		} else {
			key := sortedRows[len(page.Services)-1]
			page.NextCursor, err = encodeServiceSortCursor(serviceSortCursor{Version: 1, Filter: filter, Number: key.SortNumber, Text: key.SortText, ID: last.ID})
		}
		if err != nil {
			return ServiceCatalogPage{}, err
		}
	}
	counts, err := q.CatalogServiceCounts(ctx)
	if err != nil {
		return ServiceCatalogPage{}, err
	}
	page.All = int(counts.Total)
	page.WithSlos = int(counts.WithSlos)
	if json.Unmarshal(counts.Owners, &page.Owners) != nil {
		return ServiceCatalogPage{}, errors.New("storage: invalid service owners")
	}
	slices.Sort(page.Owners)
	total, err := q.CatalogFilteredServiceCount(ctx, dbgen.CatalogFilteredServiceCountParams{QueryText: input.Query, OwnerName: input.Owner, Coverage: input.Coverage})
	if err != nil {
		return ServiceCatalogPage{}, err
	}
	page.Total = int(total)
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return ServiceCatalogPage{}, err
	}
	return page, nil
}
