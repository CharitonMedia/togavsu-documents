package documents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Assemble and generate, carried from DocForge's rules: nothing ships with an unresolved
// token, a run is all or nothing (one bulkFile call), every run makes new draft files,
// and a failed run reports each template and token by name. In code a single-template
// run is a package of one.

// generateInput is the assemble form.
type generateInput struct {
	TemplateID      string            `json:"template_id"`
	PackageID       string            `json:"package_id"`
	ClientContactID string            `json:"client_contact_id"`
	ServedPartyID   string            `json:"served_party_id"`
	DocumentTitle   string            `json:"document_title"`
	FilingDate      string            `json:"filing_date"`
	Values          map[string]string `json:"values"`
}

// prepared is one template of a run, its body already stitched with its components.
type prepared struct {
	T          Template
	Doc        *docx
	Version    int
	Components map[string]int
	Tokens     []string
}

// Missing names one token a template still needs.
type Missing struct {
	TemplateID   string `json:"template_id"`
	TemplateName string `json:"template_name"`
	Token        string `json:"token"`
	Reason       string `json:"reason"`
}

// runItems loads the run's templates in order. An archived template, package or
// component is a 409 and nothing is written.
func (s *Server) runItems(ctx context.Context, in generateInput) ([]Template, *Package, error) {
	switch {
	case in.TemplateID != "" && in.PackageID != "":
		return nil, nil, unprocessable("pick one template or one package", nil)
	case in.TemplateID != "":
		t, err := s.template(ctx, in.TemplateID)
		if err != nil {
			return nil, nil, err
		}
		if t.Status != statusActive {
			return nil, nil, conflict("the template " + t.Name + " is archived")
		}
		return []Template{t}, nil, nil
	case in.PackageID != "":
		p, err := s.pkg(ctx, in.PackageID)
		if err != nil {
			return nil, nil, err
		}
		if p.Status != statusActive {
			return nil, nil, conflict("the package " + p.Name + " is archived")
		}
		var out []Template
		for _, id := range p.TemplateIDs {
			t, err := s.template(ctx, id)
			if err != nil {
				return nil, nil, err
			}
			if t.Status != statusActive {
				return nil, nil, conflict("the package's template " + t.Name + " is archived")
			}
			out = append(out, t)
		}
		return out, &p, nil
	}
	return nil, nil, unprocessable("pick a template or a package", nil)
}

// prepare reads each template body and its components and stitches the slots. A
// component a slot can't copy refuses the run, naming the template and the part.
func (s *Server) prepare(ctx context.Context, v viewer, ts []Template) ([]prepared, []Missing, error) {
	var out []prepared
	var refused []Missing
	for _, t := range ts {
		ver, b, err := s.currentVersion(ctx, v.Token, t.BodyFileID)
		if err != nil {
			return nil, nil, err
		}
		d, err := readDOCX(b)
		if err != nil {
			refused = append(refused, Missing{TemplateID: t.ID, TemplateName: t.Name, Reason: err.Error()})
			continue
		}
		slots, err := d.slots()
		if err != nil {
			refused = append(refused, Missing{TemplateID: t.ID, TemplateName: t.Name, Reason: err.Error()})
			continue
		}
		p := prepared{T: t, Doc: d, Version: ver.Number, Components: map[string]int{}}
		bodies := map[string]string{}
		var tokens []string
		for _, slot := range slots {
			cid, ok := t.Slots[slot]
			if !ok {
				refused = append(refused, Missing{TemplateID: t.ID, TemplateName: t.Name, Token: "slot:" + slot, Reason: "slot " + slot + " is not mapped"})
				continue
			}
			c, err := s.component(ctx, cid)
			if err != nil {
				return nil, nil, err
			}
			if c.Status != statusActive {
				return nil, nil, conflict("the component " + c.Name + " is archived")
			}
			cv, cb, err := s.currentVersion(ctx, v.Token, c.BodyFileID)
			if err != nil {
				return nil, nil, err
			}
			cd, err := readDOCX(cb)
			if err != nil {
				refused = append(refused, Missing{TemplateID: t.ID, TemplateName: t.Name, Reason: "component " + c.Name + ": " + err.Error()})
				continue
			}
			body, err := cd.componentBody()
			if err != nil {
				reason := err.Error()
				var pe *partError
				if errors.As(err, &pe) {
					reason = "component " + c.Name + " holds a " + pe.Part
				}
				refused = append(refused, Missing{TemplateID: t.ID, TemplateName: t.Name, Token: "slot:" + slot, Reason: reason})
				continue
			}
			bodies[slot] = body
			p.Components[slot] = cv.Number
			tokens = append(tokens, cd.tokens()...)
		}
		if len(bodies) == len(slots) {
			if err := d.fillSlots(bodies); err != nil {
				return nil, nil, err
			}
		}
		seen := map[string]bool{}
		for _, n := range append(d.tokens(), tokens...) {
			if !seen[n] {
				seen[n] = true
				p.Tokens = append(p.Tokens, n)
			}
		}
		out = append(out, p)
	}
	return out, refused, nil
}

// TokenRow is one row of the assemble form.
type TokenRow struct {
	Name       string   `json:"name"`
	Value      string   `json:"value"`
	Source     string   `json:"source"`
	NeedsInput bool     `json:"needs_input"`
	Templates  []string `json:"templates"`
}

// Assembly is what the assemble page shows.
type Assembly struct {
	MatterID          string       `json:"matter_id"`
	MatterClosed      bool         `json:"matter_closed"`
	Note              string       `json:"note,omitempty"`
	Clients           []ClientPick `json:"clients"`
	ClientContactID   string       `json:"client_contact_id"`
	NeedsClientChoice bool         `json:"needs_client_choice"`
	Parties           []partyView  `json:"parties"`
	Tokens            []TokenRow   `json:"tokens"`
	Refused           []Missing    `json:"refused"`
	Templates         []Template   `json:"templates"`
}

// ClientPick is one client party the form can pick.
type ClientPick struct {
	ContactID string `json:"contact_id"`
	Name      string `json:"name"`
	Primary   bool   `json:"primary"`
}

const closedNote = "This matter is closed. Doc Tender holds new files on closed matters until it reopens."

// assembly resolves every token of the run for the form.
func (s *Server) assembly(ctx context.Context, v viewer, matterID string, in generateInput, defaultPrimary bool) (*Assembly, []prepared, *resolution, error) {
	ts, _, err := s.runItems(ctx, in)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := s.getMatter(ctx, v.Token, matterID); err != nil {
		return nil, nil, nil, err
	}
	items, refused, err := s.prepare(ctx, v, ts)
	if err != nil {
		return nil, nil, nil, err
	}
	var titles []string
	for _, t := range ts {
		titles = append(titles, t.Name)
	}
	res, rerr := s.resolve(ctx, v.Token, resolveInput{MatterID: matterID, ClientID: in.ClientContactID, ServedPartyID: in.ServedPartyID,
		DocumentTitle: in.DocumentTitle, FilingDate: in.FilingDate, TemplateTitles: titles, DefaultPrimary: defaultPrimary})
	if rerr != nil && !errors.Is(rerr, errChooseClient) {
		return nil, nil, nil, rerr
	}
	a := &Assembly{MatterID: matterID, Refused: refused, Templates: ts, Tokens: []TokenRow{}, Clients: []ClientPick{}}
	if refused == nil {
		a.Refused = []Missing{}
	}
	a.MatterClosed = res.Matter.Status == "closed"
	if a.MatterClosed {
		a.Note = closedNote
	}
	names := map[string]string{}
	for _, p := range res.Parties {
		names[p.ContactID] = p.Name
	}
	for _, c := range res.Clients {
		a.Clients = append(a.Clients, ClientPick{ContactID: c.ContactID, Name: names[c.ContactID], Primary: c.Primary})
	}
	a.Parties = res.Parties
	a.ClientContactID = res.ClientID
	a.NeedsClientChoice = len(res.Clients) > 1
	rows := map[string]*TokenRow{}
	var order []string
	for _, p := range items {
		for _, n := range p.Tokens {
			row, ok := rows[n]
			if !ok {
				row = &TokenRow{Name: n}
				if isKnown(n, res.CFKnown) {
					r := res.Values[n]
					row.Value, row.Source = r.Value, vocabulary[n]
					if row.Source == "" {
						row.Source = srcCustom
					}
					if n == "document_title" && in.DocumentTitle == "" && len(items) > 1 {
						row.Value = "(each template's name)"
					}
				} else {
					row.Source = srcManual
				}
				row.NeedsInput = strings.TrimSpace(row.Value) == ""
				rows[n] = row
				order = append(order, n)
			}
			row.Templates = append(row.Templates, p.T.Name)
		}
	}
	for _, n := range order {
		a.Tokens = append(a.Tokens, *rows[n])
	}
	return a, items, res, rerr
}

func (s *Server) assembleAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	q := r.URL.Query()
	in := generateInput{TemplateID: q.Get("template_id"), PackageID: q.Get("package_id"), ClientContactID: q.Get("client_contact_id"),
		ServedPartyID: q.Get("served_party_id"), DocumentTitle: q.Get("document_title"), FilingDate: q.Get("filing_date")}
	a, _, _, err := s.assembly(r.Context(), v, r.PathValue("matter_id"), in, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// Run is one generation run.
type Run struct {
	ID           string    `json:"id"`
	MatterID     string    `json:"matter_id"`
	TemplateID   string    `json:"template_id,omitempty"`
	PackageID    string    `json:"package_id,omitempty"`
	Outcome      string    `json:"outcome"`
	FileCount    int       `json:"file_count"`
	ManualTokens []string  `json:"manual_tokens"`
	ErrorReport  []Missing `json:"error_report"`
	FileIDs      []string  `json:"file_ids"`
	PDFFileIDs   []string  `json:"pdf_file_ids"`
	StartedAt    int64     `json:"started_at"`
	FinishedAt   int64     `json:"finished_at"`
}

// saveFailed records a failed run: names only, never values.
func (s *Server) saveFailed(ctx context.Context, run *Run, v viewer, principal string) error {
	run.Outcome = "failed"
	run.FinishedAt = s.now()
	report, err := json.Marshal(run.ErrorReport)
	if err != nil {
		return err
	}
	_, err = s.c.DB.ExecContext(ctx, `INSERT INTO generation_runs (id, matter_id, package_id, template_id, actor_user_id, principal_contact_id, outcome, error_report, manual_tokens, file_count, started_at, finished_at)
VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), 'failed', $7, $8, 0, $9, $10)`,
		run.ID, run.MatterID, run.PackageID, run.TemplateID, v.UserID, principal, report, nonNil(run.ManualTokens), run.StartedAt, run.FinishedAt)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// arrivalName is the name a generated draft arrives with; the core adds a clash suffix
// and Doc Tender sets the final name.
func (s *Server) arrivalName(t Template, res *resolution) string {
	return t.Slug + "-" + res.Today.Format("2006-01-02") + ".docx"
}

func (s *Server) generateAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	var in generateInput
	if err := bind(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	run, err := s.generate(r.Context(), v, r.PathValue("matter_id"), in)
	if err != nil {
		if run != nil {
			status, body := statusOf(err)
			body["run_id"] = run.ID
			if ret := wantsPage(r); ret != "" || status >= 500 {
				s.fail(w, r, err)
				return
			}
			writeJSON(w, status, body)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.done(w, r, http.StatusCreated, run)
}

// generate runs one template or package on a matter. It returns the run when one was
// recorded (succeeded, pdf_failed, or failed after the form was checked).
func (s *Server) generate(ctx context.Context, v viewer, matterID string, in generateInput) (*Run, error) {
	run := &Run{ID: s.id(), MatterID: matterID, TemplateID: in.TemplateID, PackageID: in.PackageID, StartedAt: s.now(),
		ManualTokens: []string{}, ErrorReport: []Missing{}, FileIDs: []string{}, PDFFileIDs: []string{}}
	a, items, res, err := s.assembly(ctx, v, matterID, in, false)
	if errors.Is(err, errChooseClient) {
		return nil, unprocessable(errChooseClient.Error(), map[string]any{"code": "client_choice_required", "clients": a.Clients})
	}
	if err != nil {
		return nil, err
	}
	run.ErrorReport = append(run.ErrorReport, a.Refused...)
	// Every token: resolved, or typed by hand; a blank typed value is refused.
	values := map[string]string{}
	manual := map[string]bool{}
	for _, row := range a.Tokens {
		if !row.NeedsInput && row.Name != "document_title" {
			values[row.Name] = res.Values[row.Name].Value
			continue
		}
		if row.Name == "document_title" && strings.TrimSpace(in.DocumentTitle) == "" && len(items) > 1 {
			continue // each template's own name
		}
		if row.Name == "document_title" && !row.NeedsInput {
			values[row.Name] = res.Values[row.Name].Value
			continue
		}
		typed, ok := in.Values[row.Name]
		if !ok || strings.TrimSpace(typed) == "" {
			reason := "not filled"
			if ok {
				reason = "the typed value is blank"
			}
			for _, p := range items {
				for _, n := range p.Tokens {
					if n == row.Name {
						run.ErrorReport = append(run.ErrorReport, Missing{TemplateID: p.T.ID, TemplateName: p.T.Name, Token: n, Reason: reason})
					}
				}
			}
			continue
		}
		values[row.Name] = typed
		manual[row.Name] = true
	}
	for n := range manual {
		run.ManualTokens = append(run.ManualTokens, n)
	}
	sort.Strings(run.ManualTokens)
	principal := res.ClientID
	if len(run.ErrorReport) > 0 {
		if err := s.saveFailed(ctx, run, v, principal); err != nil {
			return nil, err
		}
		var parts []string
		for _, m := range run.ErrorReport {
			if m.Token != "" {
				parts = append(parts, m.TemplateName+": "+m.Token+" ("+m.Reason+")")
			} else {
				parts = append(parts, m.TemplateName+": "+m.Reason)
			}
		}
		return run, unprocessable("Nothing was generated. "+strings.Join(parts, "; "), map[string]any{"missing": run.ErrorReport})
	}

	// Render every output in memory, then stage every upload. Any failure before the one
	// bulkFile call publishes nothing; staged uploads expire on the core's schedule.
	type out struct {
		p    prepared
		name string
		up   string
	}
	var outs []out
	for _, p := range items {
		vals := func(name string) (string, bool) {
			if name == "document_title" {
				if t := strings.TrimSpace(in.DocumentTitle); t != "" {
					return t, true
				}
				return p.T.Name, true
			}
			val, ok := values[name]
			return val, ok
		}
		p.Doc.fill(vals)
		b, err := p.Doc.bytes()
		if err != nil {
			return s.failRun(ctx, run, v, principal, p.T, "the draft could not be written", err)
		}
		name := s.arrivalName(p.T, res)
		up, err := s.stageUpload(ctx, v.Token, name, docxMedia, matterID, false, b)
		if err != nil {
			return s.failRun(ctx, run, v, principal, p.T, "the core refused the upload", err)
		}
		outs = append(outs, out{p: p, name: name, up: up})
	}
	var bulk struct {
		Items []map[string]any `json:"items"`
	}
	for _, o := range outs {
		bulk.Items = append(bulk.Items, map[string]any{"upload_id": o.up, "matter_id": matterID, "name": o.name,
			"attorney_only": o.p.T.AttorneyOnly, "shared_with_client": false})
	}
	var published struct {
		Data []coreFile `json:"data"`
	}
	if err := s.call(ctx, http.MethodPost, "/api/v1/files:bulk", v.Token, bulk, &published); err != nil {
		return s.failRun(ctx, run, v, principal, Template{}, "the core refused to publish the drafts", err)
	}
	if len(published.Data) != len(outs) {
		return s.failRun(ctx, run, v, principal, Template{}, "the core published a different number of files", errors.New("bulk count"))
	}

	// Published: record the run and its files, then mark drafts and set category hints.
	run.Outcome = "succeeded"
	run.FileCount = len(outs)
	run.FinishedAt = s.now()
	tx, err := s.c.DB.BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_runs (id, matter_id, package_id, template_id, actor_user_id, principal_contact_id, outcome, error_report, manual_tokens, file_count, started_at, finished_at)
VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), 'succeeded', '[]', $7, $8, $9, $10)`,
		run.ID, matterID, run.PackageID, run.TemplateID, v.UserID, principal, run.ManualTokens, run.FileCount, run.StartedAt, run.FinishedAt); err != nil {
		return run, err
	}
	for i, f := range published.Data {
		comps, _ := json.Marshal(outs[i].p.Components)
		if _, err := tx.ExecContext(ctx, `INSERT INTO generated_files (file_id, run_id, template_id, template_version_number, component_versions, status_cache, updated_at)
VALUES ($1, $2, $3, $4, $5, 'draft', $6)`, f.ID, run.ID, outs[i].p.T.ID, outs[i].p.Version, comps, s.now()); err != nil {
			return run, err
		}
		run.FileIDs = append(run.FileIDs, f.ID)
	}
	if err := tx.Commit(); err != nil {
		return run, err
	}
	g := s.grants(ctx, v.Token)
	for i, id := range run.FileIDs {
		if _, err := s.markStatus(ctx, v.Token, id, "draft"); err != nil {
			s.c.Log.Printf("documents: run %s: mark draft on %s: %v", run.ID, id, err)
		}
		if t := outs[i].p.T; t.CategoryID != "" && g.DocTender {
			if err := s.call(ctx, http.MethodPatch, "/api/v1/files/"+url.PathEscape(id), v.Token, map[string]any{"category_id": t.CategoryID}, nil); err != nil {
				s.c.Log.Printf("documents: run %s: category hint on %s: %v", run.ID, id, err)
			}
		}
	}
	summary := "Generated " + outs[0].p.T.Name + " from template"
	if run.PackageID != "" {
		p, _ := s.pkg(ctx, run.PackageID)
		noun := "documents"
		if run.FileCount == 1 {
			noun = "document"
		}
		summary = "Generated " + strconv.Itoa(run.FileCount) + " " + noun + " from package " + p.Name
	}
	if err := s.writeEvent(ctx, v.Token, "documents.generated", matterID, summary); err != nil {
		s.c.Log.Printf("documents: run %s: timeline event: %v", run.ID, err)
	}
	// PDFs beside DOCX drafts, when the template asks and the editor permission is there.
	var want []string
	for i, id := range run.FileIDs {
		if outs[i].p.T.Output == "docx_and_pdf" && g.Editor {
			want = append(want, id)
		}
	}
	if len(want) > 0 {
		if err := s.makePDFs(ctx, v, run, want); err != nil {
			s.c.Log.Printf("documents: run %s: PDF: %v", run.ID, err)
		}
	}
	return run, nil
}

// failRun records a run that failed after the form passed, naming the template and the
// reason. Nothing was published.
func (s *Server) failRun(ctx context.Context, run *Run, v viewer, principal string, t Template, reason string, cause error) (*Run, error) {
	s.c.Log.Printf("documents: run %s failed: %s: %v", run.ID, reason, cause)
	run.ErrorReport = append(run.ErrorReport, Missing{TemplateID: t.ID, TemplateName: t.Name, Reason: reason})
	if err := s.saveFailed(ctx, run, v, principal); err != nil {
		return nil, err
	}
	status, _ := statusOf(cause)
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		return run, &inputError{Status: status, Code: "generation_failed", Msg: "Nothing was generated: " + reason}
	}
	return run, &inputError{Status: http.StatusBadGateway, Code: "generation_failed", Msg: "Nothing was generated: " + reason}
}

// makePDFs converts each DOCX to a PDF beside it. A failure leaves the DOCX published
// and marks the run pdf_failed, with Retry PDF on the run.
func (s *Server) makePDFs(ctx context.Context, v viewer, run *Run, fileIDs []string) error {
	var firstErr error
	for _, id := range fileIDs {
		var res struct {
			File coreFile `json:"file"`
		}
		if err := s.call(ctx, http.MethodPost, "/api/v1/files/"+url.PathEscape(id)+"/conversions", v.Token, map[string]string{"format": "pdf"}, &res); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := s.c.DB.ExecContext(ctx, `UPDATE generated_files SET pdf_file_id = $2, updated_at = $3 WHERE file_id = $1`, id, res.File.ID, s.now()); err != nil {
			return err
		}
		run.PDFFileIDs = append(run.PDFFileIDs, res.File.ID)
	}
	outcome := "succeeded"
	if firstErr != nil {
		outcome = "pdf_failed"
	}
	run.Outcome = outcome
	if _, err := s.c.DB.ExecContext(ctx, `UPDATE generation_runs SET outcome = $2 WHERE id = $1`, run.ID, outcome); err != nil {
		return err
	}
	return firstErr
}

// retryPDF converts the run's drafts that still have no PDF.
func (s *Server) retryPDF(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	run, err := s.run(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.getMatter(ctx, v.Token, run.MatterID); err != nil {
		s.fail(w, r, err)
		return
	}
	if run.Outcome != "pdf_failed" {
		s.fail(w, r, conflict("this run has no failed PDF"))
		return
	}
	if !s.grants(ctx, v.Token).Editor {
		s.fail(w, r, unprocessable("PDFs need the editor permission", nil))
		return
	}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT file_id FROM generated_files WHERE run_id = $1 AND pdf_file_id IS NULL AND NOT trashed ORDER BY file_id`, run.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.fail(w, r, err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := s.makePDFs(ctx, v, &run, ids); err != nil {
		s.fail(w, r, &inputError{Status: http.StatusBadGateway, Code: "pdf_failed", Msg: "the PDF could not be made; the DOCX drafts are still on the matter"})
		return
	}
	run, _ = s.run(ctx, run.ID)
	s.done(w, r, http.StatusOK, run)
}

func (s *Server) run(ctx context.Context, id string) (Run, error) {
	var run Run
	var report []byte
	var manual []string
	var tid, pid *string
	err := s.c.DB.QueryRowContext(ctx, `SELECT id, matter_id, template_id, package_id, outcome, file_count, manual_tokens, error_report, started_at, finished_at FROM generation_runs WHERE id = $1`, id).
		Scan(&run.ID, &run.MatterID, &tid, &pid, &run.Outcome, &run.FileCount, textArray(&manual), &report, &run.StartedAt, &run.FinishedAt)
	if err != nil {
		return run, notFound()
	}
	if tid != nil {
		run.TemplateID = *tid
	}
	if pid != nil {
		run.PackageID = *pid
	}
	run.ManualTokens = nonNil(manual)
	_ = json.Unmarshal(report, &run.ErrorReport)
	if run.ErrorReport == nil {
		run.ErrorReport = []Missing{}
	}
	run.FileIDs, run.PDFFileIDs = []string{}, []string{}
	rows, err := s.c.DB.QueryContext(ctx, `SELECT file_id, pdf_file_id FROM generated_files WHERE run_id = $1 ORDER BY file_id`, id)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		var p *string
		if err := rows.Scan(&f, &p); err != nil {
			return run, err
		}
		run.FileIDs = append(run.FileIDs, f)
		if p != nil {
			run.PDFFileIDs = append(run.PDFFileIDs, *p)
		}
	}
	return run, rows.Err()
}

// Pickers: active templates and packages whose practice areas include the matter's, or
// that name none.
type Pickers struct {
	Templates []Template `json:"templates"`
	Packages  []Package  `json:"packages"`
}

func (s *Server) pickers(ctx context.Context, m coreMatter) (Pickers, error) {
	p := Pickers{Templates: []Template{}, Packages: []Package{}}
	ts, err := s.templates(ctx, statusActive)
	if err != nil {
		return p, err
	}
	for _, t := range ts {
		if inArea(t.PracticeAreaIDs, m.PracticeAreaID) {
			p.Templates = append(p.Templates, t)
		}
	}
	ps, err := s.packages(ctx, statusActive)
	if err != nil {
		return p, err
	}
	for _, pk := range ps {
		if inArea(pk.PracticeAreaIDs, m.PracticeAreaID) {
			p.Packages = append(p.Packages, pk)
		}
	}
	return p, nil
}

func (s *Server) pickersAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	m, err := s.getMatter(r.Context(), v.Token, r.PathValue("matter_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.pickers(r.Context(), m)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// recentRuns lists the matter's runs, newest first.
func (s *Server) recentRuns(ctx context.Context, matterID string, limit int) ([]Run, error) {
	rows, err := s.c.DB.QueryContext(ctx, `SELECT id FROM generation_runs WHERE matter_id = $1 ORDER BY finished_at DESC, id DESC LIMIT $2`, matterID, limit)
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
	out := []Run{}
	for _, id := range ids {
		run, err := s.run(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Server) runsAPI(w http.ResponseWriter, r *http.Request, v viewer) {
	if _, err := s.getMatter(r.Context(), v.Token, r.PathValue("matter_id")); err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.recentRuns(r.Context(), r.PathValue("matter_id"), 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": runs})
}
