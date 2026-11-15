package alerthttp

import "net/http"

func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /api/v1/alert-policies", h.listPolicies)
	h.mux.HandleFunc("POST /api/v1/alert-policies", h.createPolicy)
	h.mux.HandleFunc("GET /api/v1/alert-policies/{policy_id}", h.getPolicy)
	h.mux.HandleFunc("PATCH /api/v1/alert-policies/{policy_id}", h.updatePolicy)
	for _, action := range []string{"enable", "disable", "retry"} {
		h.mux.HandleFunc("POST /api/v1/alert-policies/{policy_id}/"+action, func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("action", action); h.policyCommand(w, r) })
	}
	h.mux.HandleFunc("GET /api/v1/alert-policies/{policy_id}/ranges", h.ranges)
	h.mux.HandleFunc("GET /api/v1/alerts", h.listAlerts)
	h.mux.HandleFunc("GET /api/v1/alerts/{occurrence_id}", h.getAlert)
	h.mux.HandleFunc("POST /api/v1/alerts/{occurrence_id}/acknowledge", h.acknowledge)
	h.mux.HandleFunc("GET /api/v1/alerts/{occurrence_id}/evidence", h.evidence)
	h.mux.HandleFunc("GET /api/v1/alerts/{occurrence_id}/transitions", h.transitions)
	h.mux.HandleFunc("GET /api/v1/alerts/{occurrence_id}/series", h.series)
	h.mux.HandleFunc("GET /api/v1/alert-processing", h.processing)
	h.mux.HandleFunc("GET /api/v1/alert-commands", h.commandResult)
}
