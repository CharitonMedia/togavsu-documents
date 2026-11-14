package documents

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Attorney profiles: admin only. Lists active users with the attorney role from the
// user directory (CC-3) and keeps what a signature block needs. The module never
// guesses a courtesy title.

var barState = regexp.MustCompile(`^[A-Z]{2}$`)

// Attorney is one row of the Attorney profiles page.
type Attorney struct {
	UserID        string `json:"user_id"`
	Name          string `json:"name"`
	BarNumber     string `json:"bar_number"`
	BarState      string `json:"bar_state"`
	CourtesyTitle string `json:"courtesy_title"`
	SignatureLine string `json:"signature_line"`
}

func (s *Server) attorneys(ctx context.Context, v viewer) ([]Attorney, error) {
	users, err := listAll[dirUser](ctx, s, "/api/v1/user-directory", url.Values{"role": {"attorney"}, "active": {"true"}}, v.Token)
	if err != nil {
		return nil, err
	}
	out := []Attorney{}
	for _, u := range users {
		if !u.Active {
			continue
		}
		p, err := s.profile(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Attorney{UserID: u.ID, Name: u.Name, BarNumber: p.BarNumber, BarState: p.BarState, CourtesyTitle: p.CourtesyTitle, SignatureLine: p.SignatureLine})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *Server) attorneysAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	list, err := s.attorneys(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type attorneyInput struct {
	BarNumber     string `json:"bar_number"`
	BarState      string `json:"bar_state"`
	CourtesyTitle string `json:"courtesy_title"`
	SignatureLine string `json:"signature_line"`
}

func (s *Server) saveAttorney(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in attorneyInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.BarNumber = strings.TrimSpace(in.BarNumber)
	in.BarState = strings.ToUpper(strings.TrimSpace(in.BarState))
	in.CourtesyTitle = strings.TrimSpace(in.CourtesyTitle)
	in.SignatureLine = strings.TrimSpace(in.SignatureLine)
	if in.BarState != "" && !barState.MatchString(in.BarState) {
		s.fail(w, r, unprocessable("the bar state is two letters, like TX", nil))
		return
	}
	id := r.PathValue("user_id")
	list, err := s.attorneys(ctx, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var name string
	for _, a := range list {
		if a.UserID == id {
			name = a.Name
		}
	}
	if name == "" {
		s.fail(w, r, notFound())
		return
	}
	if _, err := s.c.DB.ExecContext(ctx, `INSERT INTO attorney_profiles (user_id, bar_number, bar_state, courtesy_title, signature_line, updated_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE SET bar_number = EXCLUDED.bar_number, bar_state = EXCLUDED.bar_state, courtesy_title = EXCLUDED.courtesy_title, signature_line = EXCLUDED.signature_line, updated_at = EXCLUDED.updated_at`,
		id, in.BarNumber, in.BarState, in.CourtesyTitle, in.SignatureLine, s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, Attorney{UserID: id, Name: name, BarNumber: in.BarNumber, BarState: in.BarState, CourtesyTitle: in.CourtesyTitle, SignatureLine: in.SignatureLine})
}
