// ABOUTME: GET /api/v1/sessions/{id}/tool-calls returns a
// ABOUTME: flat list of tool calls across all messages in a session.
package server

import "net/http"

func (s *Server) handleToolCalls(
	w http.ResponseWriter, r *http.Request,
) {
	id := r.PathValue("id")
	list, err := s.sessions.ToolCalls(r.Context(), id)
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}
