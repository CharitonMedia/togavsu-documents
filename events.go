package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/CharitonMedia/togavsu-documents/internal/modkit"
)

// Events and Release. The core delivers file.updated and file.trashed signed with the
// module's webhook secret. Each event id is handled once. A tracked file's status cache
// is marked stale on file.updated, because the module has no service read of a file
// (getFile is a viewer call); the next viewer read refreshes it.

const maxEventBytes = 1 << 20

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBytes))
	if err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	skew := s.c.SkewSeconds
	if skew < one {
		skew = webhookSkewSeconds
	}
	if err := modkit.VerifyWebhook(s.c.WebhookSecret, r.Header.Get(modkit.SignatureHeader), body, s.c.Now(), int64(skew)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "bad_signature", "message": "the event signature does not verify"})
		return
	}
	var ev modkit.EventPayload
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "validation_failed", "message": "the event is not readable"})
		return
	}
	handled, err := s.handleEvent(r.Context(), ev)
	if err != nil {
		s.c.Log.Printf("documents: event %s: %v", ev.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "internal", "message": "the event could not be recorded"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"handled": handled})
}

// handleEvent records the event once and applies it. It reports false for a repeat.
func (s *Server) handleEvent(ctx context.Context, ev modkit.EventPayload) (bool, error) {
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO received_events (event_id, action, record_id, received_at) VALUES ($1, $2, $3, $4) ON CONFLICT (event_id) DO NOTHING`,
		ev.ID, ev.Action, ev.RecordID, s.now())
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == int64(0) {
		return false, nil
	}
	switch ev.Action {
	case "file.updated":
		_, err = tx.ExecContext(ctx, `UPDATE generated_files SET status_cache = 'stale', updated_at = $2 WHERE file_id = $1`, ev.RecordID, s.now())
	case "file.trashed":
		_, err = tx.ExecContext(ctx, `UPDATE generated_files SET trashed = true, updated_at = $2 WHERE file_id = $1`, ev.RecordID, s.now())
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Held is one trashed file the module still points at.
type Held struct {
	FileID string   `json:"file_id"`
	Name   string   `json:"name"`
	Kinds  []string `json:"kinds"`
	// ConfirmName is the template or component name the admin must type; empty when
	// no name is needed.
	ConfirmName string `json:"confirm_name,omitempty"`
}

// holds says which module rows point at a file.
func (s *Server) holds(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Held, error) {
	h := Held{FileID: id, Kinds: []string{}}
	checks := []struct{ kind, sql string }{
		{"generated file", `SELECT count(*) FROM generated_files WHERE file_id = $1`},
		{"generated PDF", `SELECT count(*) FROM generated_files WHERE pdf_file_id = $1`},
		{"zip manifest", `SELECT count(*) FROM bulk_download_items WHERE file_id = $1`},
		{"version notes", `SELECT count(*) FROM version_notes WHERE file_id = $1`},
		{"version copy", `SELECT count(*) FROM version_copies WHERE file_id = $1 OR source_file_id = $1`},
	}
	for _, c := range checks {
		var n int
		if err := q.QueryRowContext(ctx, c.sql, id).Scan(&n); err != nil {
			return h, err
		}
		if n > zero {
			h.Kinds = append(h.Kinds, c.kind)
		}
	}
	var name string
	switch err := q.QueryRowContext(ctx, `SELECT name FROM templates WHERE body_file_id = $1`, id).Scan(&name); err {
	case nil:
		h.Kinds = append(h.Kinds, "template body")
		h.ConfirmName = name
	case sql.ErrNoRows:
	default:
		return h, err
	}
	switch err := q.QueryRowContext(ctx, `SELECT name FROM components WHERE body_file_id = $1`, id).Scan(&name); err {
	case nil:
		h.Kinds = append(h.Kinds, "component body")
		h.ConfirmName = name
	case sql.ErrNoRows:
	default:
		return h, err
	}
	return h, nil
}

// held lists the trashed files the module holds. The candidates are files the core
// reported trashed; each is confirmed trashed with getFile as the admin.
func (s *Server) held(ctx context.Context, v viewer) ([]Held, error) {
	rows, err := s.c.DB.QueryContext(ctx, `SELECT DISTINCT record_id FROM received_events WHERE action = 'file.trashed'`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	sort.Strings(ids)
	out := []Held{}
	for _, id := range ids {
		h, err := s.holds(ctx, s.c.DB, id)
		if err != nil {
			return nil, err
		}
		if len(h.Kinds) == 0 {
			continue
		}
		f, err := s.getFile(ctx, v.Token, id)
		if err != nil || f.TrashedAt == nil {
			continue
		}
		h.Name = f.Name
		out = append(out, h)
	}
	return out, nil
}

func (s *Server) heldAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	list, err := s.held(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type releaseInput struct {
	ConfirmName string `json:"confirm_name"`
}

// release lets the core purge a trashed file: it drops or nulls every module row that
// points at it. Manifest entry paths and SHA-256s stay.
func (s *Server) release(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in releaseInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("file_id")
	f, err := s.getFile(ctx, v.Token, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if f.TrashedAt == nil {
		s.fail(w, r, conflict("only a trashed file can be released"))
		return
	}
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback()
	h, err := s.holds(ctx, tx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(h.Kinds) == 0 {
		s.fail(w, r, notFound())
		return
	}
	if h.ConfirmName != "" && strings.TrimSpace(in.ConfirmName) != h.ConfirmName {
		s.fail(w, r, unprocessable("type the name "+h.ConfirmName+" to release it; this deletes it from the library", nil))
		return
	}
	stmts := []string{
		`DELETE FROM templates WHERE body_file_id = $1`,
		`DELETE FROM components WHERE body_file_id = $1`,
		`DELETE FROM generated_files WHERE file_id = $1`,
		`UPDATE generated_files SET pdf_file_id = NULL WHERE pdf_file_id = $1`,
		`UPDATE bulk_download_items SET file_id = NULL WHERE file_id = $1`,
		`DELETE FROM version_notes WHERE file_id = $1`,
		`DELETE FROM version_copies WHERE file_id = $1 OR source_file_id = $1`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, map[string]any{"file_id": id, "released": h.Kinds})
}
