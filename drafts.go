package documents

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Drafts: generated files the viewer can see, by status. Visibility is checked per row
// with getFile as the viewer; a row the core hides is skipped. The status shows as a tag
// and never enters the name.

// Draft is one row of the Drafts tab and widget.
type Draft struct {
	FileID       string `json:"file_id"`
	Name         string `json:"name"`
	MatterID     string `json:"matter_id"`
	Status       string `json:"status"`
	TemplateID   string `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	RunID        string `json:"run_id"`
	GeneratedAt  int64  `json:"generated_at"`
	PDFFileID    string `json:"pdf_file_id,omitempty"`
	DocTender    string `json:"doc_tender_status,omitempty"`
}

type draftQuery struct {
	MatterID string
	Status   string // draft (default) or final
	Since    int64  // generated at or after, unix seconds; 0 for any
	Limit    int    // 0 for no limit
}

// drafts lists the generated files the viewer can see whose live status matches.
func (s *Server) drafts(ctx context.Context, v viewer, q draftQuery) ([]Draft, error) {
	if q.Status == "" {
		q.Status = "draft"
	}
	sqlq := `SELECT g.file_id, r.matter_id, COALESCE(g.template_id, ''), COALESCE(t.name, ''), g.run_id, r.finished_at, COALESCE(g.pdf_file_id, '')
FROM generated_files g JOIN generation_runs r ON r.id = g.run_id LEFT JOIN templates t ON t.id = g.template_id
WHERE NOT g.trashed AND r.finished_at >= $1`
	args := []any{q.Since}
	if q.MatterID != "" {
		sqlq += ` AND r.matter_id = $2`
		args = append(args, q.MatterID)
	}
	sqlq += ` ORDER BY r.finished_at DESC, g.file_id DESC`
	rows, err := s.c.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	var cands []Draft
	for rows.Next() {
		var d Draft
		if err := rows.Scan(&d.FileID, &d.MatterID, &d.TemplateID, &d.TemplateName, &d.RunID, &d.GeneratedAt, &d.PDFFileID); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	g := s.grants(ctx, v.Token)
	out := []Draft{}
	for _, d := range cands {
		f, err := s.getFile(ctx, v.Token, d.FileID)
		if err != nil {
			if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusForbidden) {
				continue
			}
			return nil, err
		}
		if f.TrashedAt != nil {
			continue
		}
		d.Name, d.Status = f.Name, f.status()
		// The live read refreshes the cache that file.updated marked stale.
		_, _ = s.c.DB.ExecContext(ctx, `UPDATE generated_files SET status_cache = $2, updated_at = $3 WHERE file_id = $1 AND status_cache <> $2`, d.FileID, d.Status, s.now())
		if d.Status != q.Status {
			continue
		}
		if g.DocTender {
			if st, err := s.docTenderStatus(ctx, v.Token, d.FileID); err == nil {
				d.DocTender = st.Label
				if d.DocTender == "" {
					d.DocTender = st.Status
				}
			}
		}
		out = append(out, d)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

func (s *Server) draftsAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	st := q.Get("status")
	if st != "" && st != "draft" && st != "final" {
		s.fail(w, r, unprocessable("status is draft or final", nil))
		return
	}
	if m := q.Get("matter_id"); m != "" {
		if _, err := s.getMatter(r.Context(), v.Token, m); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	list, err := s.drafts(r.Context(), v, draftQuery{MatterID: q.Get("matter_id"), Status: st})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// widgetDrafts is the widget's list: top eight drafts of the last drafts_window_days.
func (s *Server) widgetDrafts(ctx context.Context, v viewer) ([]Draft, int, error) {
	days, err := s.intSetting(ctx, "drafts_window_days")
	if err != nil || days <= 0 {
		days = 30
	}
	since := s.c.Now().Add(-durationDays(days)).Unix()
	list, err := s.drafts(ctx, v, draftQuery{Status: "draft", Since: since, Limit: 8})
	return list, days, err
}

func (s *Server) widgetAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	list, days, err := s.widgetDrafts(r.Context(), v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "window_days": days})
}

type statusInput struct {
	Status string `json:"status"`
}

// setDraftStatus marks a generated file final or draft through markFileStatus.
func (s *Server) setDraftStatus(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	var in statusInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Status != "draft" && in.Status != "final" {
		s.fail(w, r, unprocessable("status is draft or final", nil))
		return
	}
	id := r.PathValue("file_id")
	var n int
	if err := s.c.DB.QueryRowContext(ctx, `SELECT count(*) FROM generated_files WHERE file_id = $1`, id).Scan(&n); err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, notFound())
		return
	}
	f, err := s.markStatus(ctx, v.Token, id, in.Status)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.c.DB.ExecContext(ctx, `UPDATE generated_files SET status_cache = $2, updated_at = $3 WHERE file_id = $1`, id, f.status(), s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusOK, map[string]any{"file_id": id, "status": f.status()})
}

// drafts tab link the widget's click-through opens.
func draftsTabLink(matterID string) string {
	return "/modules/" + ID + "/tabs/drafts?" + url.Values{"matter_id": {matterID}}.Encode()
}

func durationDays(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
