package slihttp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage"
)

func (h *Handler) backfillAgent(w http.ResponseWriter, r *http.Request, requestID string, claim bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Cookie")) != 0 ||
		len(r.Header.Values("Origin")) != 0 || h.auth.ValidateTLSRequest(r) != nil {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	generations := r.Header.Values("X-Agent-Session-Generation")
	proofHeaders := r.Header.Values("X-Agent-Session-Proof")
	if !bearerHeader(r.Header.Values("Authorization")) || len(generations) != 1 || len(proofHeaders) != 1 {
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	secret, proofErr := base64.RawURLEncoding.Strict().DecodeString(proofHeaders[0])
	generation, err := strconv.ParseInt(generations[0], 10, 64)
	credential, verifier, credentialErr := identity.AgentTokenVerifier(bearerToken(r.Header.Get("Authorization")))
	if err != nil || generation < 1 || strconv.FormatInt(generation, 10) != generations[0] || credentialErr != nil || proofErr != nil || len(secret) != 32 || len(proofHeaders[0]) != 43 {
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	proof := storage.BackfillAgentProof{CredentialID: credential, Verifier: verifier, ResumeVerifier: sha256.Sum256(secret), Generation: generation}
	if claim {
		if hasBody(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		result, err := storage.ClaimBackfill(r.Context(), h.databaseURL, proof, h.now, h.runtime)
		if err != nil {
			h.backfillAgentError(w, err, requestID)
		} else if result.ID == "" {
			w.WriteHeader(http.StatusNoContent)
		} else {
			h.writeJSON(w, http.StatusOK, result)
		}
		return
	}

	body, status := readBody(w, r, 128<<10)
	if status != 0 {
		h.bodyError(w, status, requestID)
		return
	}
	var result sli.BackfillChunkResult
	if json.Unmarshal(body, &result, json.RejectUnknownMembers(true)) != nil {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	if err := storage.SubmitBackfill(r.Context(), h.databaseURL, proof, h.now, result, h.runtime); err != nil {
		h.backfillAgentError(w, err, requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) backfillAgentError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, storage.ErrInvalidAgentCredential):
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
	case errors.Is(err, storage.ErrObservationDenied):
		h.writeProblem(w, http.StatusConflict, "historical_authority_unavailable", requestID)
	case errors.Is(err, storage.ErrObservationInvalid):
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
	default:
		h.writeProblem(w, http.StatusServiceUnavailable, "historical_work_unavailable", requestID)
	}
}
