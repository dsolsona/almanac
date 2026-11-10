package storage

import (
	"crypto/sha256"
	"encoding/json/v2"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
)

func sliObservationPayloadDigest(item sli.ObservationItem) ([32]byte, error) {
	payload := struct {
		Version               int                  `json:"version"`
		DeliveryID            string               `json:"delivery_id"`
		ExecutionID           string               `json:"execution_id"`
		CreatedAt             time.Time            `json:"created_at"`
		LeaseID               string               `json:"lease_id"`
		ConfigurationRevision string               `json:"configuration_revision"`
		SessionGeneration     string               `json:"session_generation"`
		SLIID                 string               `json:"sli_id"`
		MeasurementRevision   string               `json:"measurement_revision"`
		IntervalStart         time.Time            `json:"interval_start"`
		IntervalEnd           time.Time            `json:"interval_end"`
		Outcome               sli.ExecutionOutcome `json:"outcome"`
	}{1, item.DeliveryID, item.ExecutionID, item.CreatedAt.UTC(), item.LeaseID,
		item.ConfigurationRevision, item.SessionGeneration, item.SLIID, item.MeasurementRevision,
		item.IntervalStart.UTC(), item.IntervalEnd.UTC(), sli.CanonicalOutcome(item.Outcome)}
	data, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, err
	}
	digest := sha256.Sum256(data)
	clear(data)
	return digest, nil
}
