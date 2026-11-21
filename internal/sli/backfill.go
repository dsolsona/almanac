package sli

import "time"

// BackfillClaim identifies one bounded historical query range for an assigned agent.
type BackfillClaim struct {
	ID           string    `json:"id"`
	SLIID        string    `json:"sli_id"`
	DatasourceID string    `json:"datasource_id"`
	Generation   int64     `json:"generation"`
	Revision     int64     `json:"measurement_revision"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Cadence      int       `json:"cadence_seconds"`
	LeaseUntil   time.Time `json:"lease_until"`
}

// BackfillChunkResult uses the claim's cadence and order for complete outcomes.
type BackfillChunkResult struct {
	ChunkID    string             `json:"chunk_id"`
	Generation int64              `json:"generation"`
	Outcomes   []ExecutionOutcome `json:"outcomes,omitempty"`
	Failure    string             `json:"failure,omitempty"`
}
