package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Key management is never forwarded by webd. Only local socket clients can use it.
func (s *Server) handleBotKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Op string `json:"op"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, Error{Error: "invalid_request"})
		return
	}
	out, err := s.db.ManageBotKey(r.Context(), in.Op)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}

func (c *Client) BotKey(ctx context.Context, op string) (store.BotKeyResponse, error) {
	var out store.BotKeyResponse
	err := c.post(ctx, "/bot-key", map[string]string{"op": op}, &out)
	return out, err
}

// Bot API routes are separate from the website's session-authenticated API.
// The caller can select dates, but cannot override the sole owner's identity,
// group or subgroup, or reach arbitrary private socket handlers.
func (s *Server) handlePersonalBot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, Error{Error: "method_not_allowed"})
		return
	}
	var handler http.HandlerFunc
	param := ""
	switch r.URL.Path {
	case "/bot/me":
	case "/bot/schedule/day":
		handler, param = s.handleDay, "date"
	case "/bot/schedule/week":
		handler, param = s.handleWeek, "monday"
	case "/bot/schedule/now":
		handler = s.handleNow
	case "/bot/schedule/next-lesson":
		handler, param = s.handleNextLesson, "from"
	case "/bot/schedule/upcoming":
		handler, param = s.handleUpcoming, "date"
	case "/bot/schedule/exams":
		handler = s.handleExams
	case "/bot/changes/dates":
		handler = s.handleChangeDates
	case "/bot/changes/day":
		handler, param = s.handleChangeDay, "date"
	default:
		writeJSON(w, 404, Error{Error: "not_found"})
		return
	}
	headers := r.Header.Values("Authorization")
	var token string
	if len(headers) == 1 {
		scheme, value, ok := strings.Cut(headers[0], " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			token = strings.TrimLeft(value, " ")
		}
	}
	group, subgroup, err := s.db.BotKeyProfile(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="personal-bot", error="invalid_token"`)
		writeJSON(w, 401, Error{Error: "invalid_token"})
		return
	}
	if err != nil {
		s.log.Error("bot API authentication", "error", err)
		writeJSON(w, 503, Error{Error: "unavailable"})
		return
	}
	if group <= 0 {
		writeJSON(w, 409, Error{Error: "group_not_configured"})
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 256 {
		writeJSON(w, 400, Error{Error: "invalid_query"})
		return
	}
	for key, values := range q {
		if param == "" || key != param || len(values) != 1 || !validBotParam(key, values[0], r.URL.Path, time.Now().In(s.loc).Year()) {
			writeJSON(w, 400, Error{Error: "invalid_query"})
			return
		}
	}
	if handler == nil {
		cx, err := s.resolveContext(r.Context(), group, subgroup)
		if err != nil {
			s.fail(w, 503, err)
			return
		}
		writeJSON(w, 200, struct {
			Context
			Timezone string `json:"timezone"`
			Scope    string `json:"scope"`
		}{cx, s.loc.String(), "schedule:read"})
		return
	}
	q.Set("group", strconv.FormatInt(group, 10))
	q.Set("subgroup", strconv.FormatInt(subgroup, 10))
	r = r.Clone(r.Context())
	r.URL.RawQuery = q.Encode()
	r.Header.Del("Authorization")
	r.Header.Del("Cookie")
	handler(w, r)
}

func validBotParam(key, value, path string, year int) bool {
	if key == "from" {
		return value == "tomorrow"
	}
	if path == "/bot/schedule/day" && (value == DateToday || value == DateNext) {
		return true
	}
	d, err := time.Parse("2006-01-02", value)
	return err == nil && d.Year() >= 2020 && d.Year() <= year+2
}
