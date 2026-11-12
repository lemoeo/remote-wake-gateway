package httptransport

import (
	"net/http"
	"time"
)

func (s *Server) previewHistoryCleanup(w http.ResponseWriter, r *http.Request) {
	plan, err := s.App.PreviewHistoryCleanup(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, plan)
}

func (s *Server) cleanupHistory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Scope  string    `json:"scope"`
		Before time.Time `json:"before"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.App.CleanupHistory(r.Context(), input.Scope, input.Before)
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, result)
}
