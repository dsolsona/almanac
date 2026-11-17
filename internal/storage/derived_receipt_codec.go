package storage

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/dsolsona/almanac/internal/api"
)

const receiptMetadataVersion = 2

var receiptMetadataDecoder = newReceiptMetadataDecoder()

// Receipt markers live on the objects that own the metadata, not only the
// enclosing payload: PostgreSQL also returns individual history pages and
// budget sample arrays. Only persistence codecs use these options; API JSON
// contains neither the marker nor any change to numerical evidence.
func marshalReceiptMetadata(value any) ([]byte, error) {
	return json.Marshal(value, json.WithMarshalers(receiptMarshalers()))
}

func unmarshalReceiptMetadata(data []byte, value any) error {
	return receiptMetadataDecoder(data, value)
}

type receiptOperationalQuality api.SloOperationalQuality
type receiptEvaluationQuality api.SloEvaluationQuality
type receiptOperationalSample api.SloOperationalSample
type extremaReceiptMark extremaMark

func receiptMarshalers() *json.Marshalers {
	return json.JoinMarshalers(
		json.MarshalFunc(func(value api.SloOperationalQuality) ([]byte, error) {
			return marshalReceiptMetadata(struct {
				receiptOperationalQuality
				ReceiptMetadataVersion int `json:"_receipt_metadata_version"`
			}{receiptOperationalQuality: receiptOperationalQuality(value), ReceiptMetadataVersion: receiptMetadataVersion})
		}),
		json.MarshalFunc(func(value api.SloEvaluationQuality) ([]byte, error) {
			return marshalReceiptMetadata(struct {
				receiptEvaluationQuality
				ReceiptMetadataVersion int `json:"_receipt_metadata_version"`
			}{receiptEvaluationQuality: receiptEvaluationQuality(value), ReceiptMetadataVersion: receiptMetadataVersion})
		}),
		json.MarshalFunc(func(value api.SloOperationalSample) ([]byte, error) {
			return marshalReceiptMetadata(struct {
				receiptOperationalSample
				ReceiptMetadataVersion int `json:"_receipt_metadata_version"`
			}{receiptOperationalSample: receiptOperationalSample(value), ReceiptMetadataVersion: receiptMetadataVersion})
		}),
		json.MarshalFunc(func(value extremaMark) ([]byte, error) {
			return marshalReceiptMetadata(struct {
				extremaReceiptMark
				ReceiptMetadataVersion int `json:"_receipt_metadata_version"`
			}{extremaReceiptMark: extremaReceiptMark(value), ReceiptMetadataVersion: receiptMetadataVersion})
		}),
	)
}

// Keep one immutable registry so recursive sample/quality decoding reuses its
// type-dispatch caches. Callbacks capture the local decoder to avoid a package
// initialization cycle through unmarshalReceiptMetadata.
func newReceiptMetadataDecoder() func([]byte, any) error {
	var registry *json.Unmarshalers
	decode := func(data []byte, value any) error {
		return json.Unmarshal(data, value, json.WithUnmarshalers(registry))
	}
	registry = json.JoinUnmarshalers(
		json.UnmarshalFunc(func(data []byte, value *api.SloOperationalQuality) error {
			var saved struct {
				receiptOperationalQuality
				ReceiptMetadataVersion jsontext.Value `json:"_receipt_metadata_version"`
			}
			if err := decode(data, &saved); err != nil {
				return err
			}
			*value = api.SloOperationalQuality(saved.receiptOperationalQuality)
			// Unknown and malformed marker values quarantine metadata, not measurements.
			if string(saved.ReceiptMetadataVersion) != "2" {
				value.LatestReceiptAt = nil
			}
			return nil
		}),
		json.UnmarshalFunc(func(data []byte, value *api.SloEvaluationQuality) error {
			var saved struct {
				receiptEvaluationQuality
				ReceiptMetadataVersion jsontext.Value `json:"_receipt_metadata_version"`
			}
			if err := decode(data, &saved); err != nil {
				return err
			}
			*value = api.SloEvaluationQuality(saved.receiptEvaluationQuality)
			// Unknown and malformed marker values quarantine metadata, not measurements.
			if string(saved.ReceiptMetadataVersion) != "2" {
				value.LatestReceiptAt = nil
			}
			return nil
		}),
		json.UnmarshalFunc(func(data []byte, value *api.SloOperationalSample) error {
			var saved struct {
				receiptOperationalSample
				ReceiptMetadataVersion jsontext.Value `json:"_receipt_metadata_version"`
			}
			if err := decode(data, &saved); err != nil {
				return err
			}
			*value = api.SloOperationalSample(saved.receiptOperationalSample)
			// Unknown and malformed marker values quarantine metadata, not measurements.
			if string(saved.ReceiptMetadataVersion) != "2" {
				value.ReceivedAt = nil
			}
			return nil
		}),
		json.UnmarshalFunc(func(data []byte, value *extremaMark) error {
			var saved struct {
				extremaReceiptMark
				ReceiptMetadataVersion jsontext.Value `json:"_receipt_metadata_version"`
			}
			if err := decode(data, &saved); err != nil {
				return err
			}
			*value = extremaMark(saved.extremaReceiptMark)
			// Unknown and malformed marker values quarantine metadata, not measurements.
			if string(saved.ReceiptMetadataVersion) != "2" {
				value.LatestReceipt = nil
			}
			return nil
		}),
	)
	return decode
}
