package api

import (
	"context"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"net/http"
)

func (s *Server) handleWebAuth(w http.ResponseWriter, r *http.Request) {
	var in store.WebAuthRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid"})
		return
	}
	out, err := s.db.WebAuth(r.Context(), in)
	if err != nil {
		s.log.Error("web auth storage", "error", err)
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	writeJSON(w, 200, out)
}
func (c *Client) WebAuth(ctx context.Context, in store.WebAuthRequest) (store.WebAuthResponse, error) {
	var out store.WebAuthResponse
	err := c.post(ctx, "/web-auth", in, &out)
	return out, err
}
