package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The field vocabulary, carried from Unified's field-vocabulary.ts and mapped to
// Togavsu's records (matter_internal_number is matter_number). A token outside it is a
// manual fill the person types on the assemble form.

// Token sources, shown beside each value on the assemble page.
const (
	srcMatter   = "Matter"
	srcClient   = "Client"
	srcOpposing = "Opposing party"
	srcServed   = "Served party"
	srcFirm     = "Firm"
	srcAttorney = "Attorney"
	srcDocument = "Document"
	srcCustom   = "Custom field"
	srcManual   = "Typed on the form"
)

// vocabulary maps each known token to its source.
var vocabulary = map[string]string{
	"matter_number": srcMatter, "matter_name": srcMatter, "matter_case_number": srcMatter, "matter_court": srcMatter,
	"matter_county": srcMatter, "matter_client_position": srcMatter,
	"client_full_name": srcClient, "client_first_name": srcClient, "client_last_name": srcClient,
	"client_also_known_as": srcClient, "client_address_line_1": srcClient, "client_address_line_2": srcClient,
	"client_city": srcClient, "client_state": srcClient, "client_zip": srcClient,
	"opposing_party_full_name": srcOpposing, "opposing_party_role": srcOpposing,
	"served_party_name": srcServed, "served_party_address": srcServed,
	"firm_name": srcFirm, "firm_address": srcFirm, "firm_phone": srcFirm, "firm_email": srcFirm, "firm_website": srcFirm,
	"attorney_name": srcAttorney, "attorney_last_name": srcAttorney, "attorney_bar_number": srcAttorney,
	"attorney_bar_state": srcAttorney, "attorney_signature_line": srcAttorney, "attorney_courtesy_name": srcAttorney,
	"attorney_for_role": srcAttorney,
	"document_title":    srcDocument, "today_long": srcDocument, "filing_date_long": srcDocument,
}

// VocabularyNames lists the known tokens, sorted.
func VocabularyNames() []string {
	out := make([]string, 0, len(vocabulary))
	for k := range vocabulary {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isKnown reports whether a token fills from records: the vocabulary, or a custom field
// of the matter or the client among defs.
func isKnown(name string, defs map[string]bool) bool {
	if _, ok := vocabulary[name]; ok {
		return true
	}
	return defs[name]
}

// Role keys the resolver reads.
const (
	roleClient   = "client"
	roleOpposing = "opposing_party"
)

// longDate is "November 12, 2026".
func longDate(t time.Time) string { return t.Format("January 2, 2006") }

func ordinal(n int) string {
	suffix := "th"
	// 11th, 12th and 13th; otherwise 1st, 2nd, 3rd by the last digit.
	if r := n % (ten * ten); r < ten+one || r > ten+three {
		switch n % ten {
		case one:
			suffix = "st"
		case two:
			suffix = "nd"
		case three:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// filingDate is "12th day of November, 2026".
func filingDate(t time.Time) string {
	return ordinal(t.Day()) + " day of " + t.Format("January, 2006")
}

// Resolved is one token's value and where it came from. A blank value counts as
// unresolved.
type Resolved struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// resolveInput is what the assemble form chose.
type resolveInput struct {
	MatterID       string
	ClientID       string // chosen client party's contact id; empty means the only one
	ServedPartyID  string
	DocumentTitle  string
	FilingDate     string // YYYY-MM-DD; empty is today
	TemplateTitles []string
	// DefaultPrimary picks the primary client when the matter has several and none was
	// chosen: the assemble page shows that default; Generate needs a real choice.
	DefaultPrimary bool
}

// resolution is everything the resolver read for one matter.
type resolution struct {
	Values   map[string]Resolved
	CFKnown  map[string]bool
	Clients  []coreParty
	Parties  []partyView
	Matter   coreMatter
	ClientID string
	Zone     *time.Location
	Today    time.Time
}

type partyView struct {
	ID, ContactID, Role, Name string
}

// errChooseClient is returned when the matter has more than one client and no choice.
var errChooseClient = errors.New("the matter has more than one client; choose which client fills the client tokens")

// resolve reads the matter, its parties, the firm and the attorney through the API as
// the viewer, and fills the vocabulary.
func (s *Server) resolve(ctx context.Context, tok string, in resolveInput) (*resolution, error) {
	m, err := s.getMatter(ctx, tok, in.MatterID)
	if err != nil {
		return nil, err
	}
	firm, err := s.firm(ctx, tok)
	if err != nil {
		return nil, err
	}
	zone, err := time.LoadLocation(firm.TimeZone)
	if err != nil || firm.TimeZone == "" {
		zone = time.UTC
	}
	r := &resolution{Values: map[string]Resolved{}, CFKnown: map[string]bool{}, Matter: m, Zone: zone, Today: s.c.Now().In(zone)}
	set := func(name, v string) { r.Values[name] = Resolved{Value: strings.TrimSpace(v), Source: vocabulary[name]} }

	set("matter_number", m.Number)
	set("matter_name", m.Name)
	set("matter_case_number", m.CaseNumber)
	set("matter_court", m.Court)
	set("matter_client_position", m.ClientPosition)
	county := ""
	if m.CountyID != nil && *m.CountyID != "" {
		var c struct {
			Name string `json:"name"`
		}
		if err := s.call(ctx, http.MethodGet, "/api/v1/counties/"+url.PathEscape(*m.CountyID), tok, nil, &c); err != nil {
			return nil, err
		}
		county = c.Name
	}
	set("matter_county", county)

	parties, err := listAll[coreParty](ctx, s, "/api/v1/matter-parties", url.Values{"matter_id": {m.ID}}, tok)
	if err != nil {
		return nil, err
	}
	contacts := map[string]coreContact{}
	contact := func(id string) (coreContact, error) {
		if c, ok := contacts[id]; ok {
			return c, nil
		}
		var c coreContact
		if err := s.call(ctx, http.MethodGet, "/api/v1/contacts/"+url.PathEscape(id), tok, nil, &c); err != nil {
			return c, err
		}
		contacts[id] = c
		return c, nil
	}
	for _, p := range parties {
		c, err := contact(p.ContactID)
		if err != nil {
			if isStatus(err, http.StatusNotFound) {
				continue
			}
			return nil, err
		}
		r.Parties = append(r.Parties, partyView{ID: p.ID, ContactID: p.ContactID, Role: p.Role, Name: c.FullName})
		if p.Role == roleClient {
			r.Clients = append(r.Clients, p)
		}
	}
	// The client: the one chosen on the form; with one client, that one.
	var client *coreParty
	switch {
	case in.ClientID != "":
		for i := range r.Clients {
			if r.Clients[i].ContactID == in.ClientID || r.Clients[i].ID == in.ClientID {
				client = &r.Clients[i]
			}
		}
		if client == nil {
			return nil, &inputError{Status: http.StatusUnprocessableEntity, Msg: "the chosen client is not a client party on this matter"}
		}
	case len(r.Clients) == one:
		client = &r.Clients[0]
	case len(r.Clients) > one && in.DefaultPrimary:
		client = &r.Clients[0]
		for i := range r.Clients {
			if r.Clients[i].Primary {
				client = &r.Clients[i]
				break
			}
		}
	case len(r.Clients) > one:
		return r, errChooseClient
	}
	var clientContact coreContact
	if client != nil {
		r.ClientID = client.ContactID
		clientContact, err = contact(client.ContactID)
		if err != nil {
			return nil, err
		}
		addr, err := s.currentAddress(ctx, tok, client.ContactID)
		if err != nil {
			return nil, err
		}
		set("client_full_name", clientContact.FullName)
		set("client_first_name", clientContact.GivenName)
		set("client_last_name", clientContact.FamilyName)
		set("client_also_known_as", strings.Join(clientContact.AlsoKnownAs, ", "))
		set("client_address_line_1", addr.Line1)
		set("client_address_line_2", addr.Line2)
		set("client_city", addr.City)
		set("client_state", addr.Region)
		set("client_zip", addr.PostalCode)
	}

	// Opposing party: the primary party with role opposing_party, else the first.
	var opp *coreParty
	for i := range parties {
		if parties[i].Role != roleOpposing {
			continue
		}
		if opp == nil || (parties[i].Primary && !opp.Primary) {
			opp = &parties[i]
		}
	}
	if opp != nil {
		if c, err := contact(opp.ContactID); err == nil {
			set("opposing_party_full_name", c.FullName)
		}
		roles, err := listAll[struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		}](ctx, s, "/api/v1/party-roles", nil, tok)
		if err != nil {
			return nil, err
		}
		for _, pr := range roles {
			if pr.Key == roleOpposing {
				set("opposing_party_role", pr.Label)
			}
		}
	}

	// Served party, picked on the form.
	if in.ServedPartyID != "" {
		var served *coreParty
		for i := range parties {
			if parties[i].ID == in.ServedPartyID {
				served = &parties[i]
			}
		}
		if served == nil {
			return nil, &inputError{Status: http.StatusUnprocessableEntity, Msg: "the served party is not a party on this matter"}
		}
		c, err := contact(served.ContactID)
		if err != nil {
			return nil, err
		}
		set("served_party_name", c.FullName)
		var addr coreAddress
		if served.ServiceAddressID != nil && *served.ServiceAddressID != "" {
			if err := s.call(ctx, http.MethodGet, "/api/v1/contact-addresses/"+url.PathEscape(*served.ServiceAddressID), tok, nil, &addr); err != nil {
				return nil, err
			}
		} else if addr, err = s.currentAddress(ctx, tok, served.ContactID); err != nil {
			return nil, err
		}
		set("served_party_address", oneLine(addr))
	}

	set("firm_name", firm.Name)
	set("firm_address", firm.Address)
	set("firm_phone", firm.Phone)
	set("firm_email", firm.Email)
	set("firm_website", firm.Website)

	// Attorney: the matter's responsible attorney.
	if m.ResponsibleAttorneyUserID != nil && *m.ResponsibleAttorneyUserID != "" {
		uid := *m.ResponsibleAttorneyUserID
		users, err := listAll[dirUser](ctx, s, "/api/v1/user-directory", url.Values{"ids": {uid}}, tok)
		if err != nil {
			return nil, err
		}
		name := ""
		for _, u := range users {
			if u.ID == uid {
				name = u.Name
			}
		}
		set("attorney_name", name)
		last := ""
		if f := strings.Fields(name); len(f) > 0 {
			last = f[len(f)-1]
		}
		set("attorney_last_name", last)
		p, err := s.profile(ctx, uid)
		if err != nil {
			return nil, err
		}
		set("attorney_bar_number", p.BarNumber)
		set("attorney_bar_state", p.BarState)
		set("attorney_signature_line", p.SignatureLine)
		courtesy := ""
		if strings.TrimSpace(p.CourtesyTitle) != "" && last != "" {
			courtesy = strings.TrimSpace(p.CourtesyTitle) + " " + last
		}
		set("attorney_courtesy_name", courtesy)
	}
	if strings.TrimSpace(m.ClientPosition) != "" {
		set("attorney_for_role", "Attorney for "+strings.TrimSpace(m.ClientPosition))
	}

	title := in.DocumentTitle
	if title == "" && len(in.TemplateTitles) == one {
		title = in.TemplateTitles[0]
	}
	set("document_title", title)
	set("today_long", longDate(r.Today))
	fd := r.Today
	if in.FilingDate != "" {
		t, err := time.ParseInLocation(time.DateOnly, in.FilingDate, zone)
		if err != nil {
			return nil, &inputError{Status: http.StatusUnprocessableEntity, Msg: "the filing date is YYYY-MM-DD"}
		}
		fd = t
	}
	set("filing_date_long", filingDate(fd))

	// Custom fields: every active field on the matter and the chosen client.
	defs, err := listAll[coreCFDef](ctx, s, "/api/v1/custom-field-definitions", nil, tok)
	if err != nil {
		return nil, err
	}
	byKey := map[string]map[string]coreCFDef{"matter": {}, "contact": {}}
	for _, d := range defs {
		if d.Retired {
			continue
		}
		if byKey[d.RecordType] != nil {
			byKey[d.RecordType][d.Key] = d
		}
		switch d.RecordType {
		case "matter":
			r.CFKnown["matter_cf_"+d.Key] = true
		case "contact":
			r.CFKnown["client_cf_"+d.Key] = true
		}
	}
	custom := func(prefix string, defs map[string]coreCFDef, values []cfValue) {
		for _, v := range values {
			d, ok := defs[v.Key]
			if !ok {
				continue
			}
			r.Values[prefix+v.Key] = Resolved{Value: strings.TrimSpace(formatCustom(d, v.Value, zone)), Source: srcCustom}
		}
	}
	custom("matter_cf_", byKey["matter"], m.CustomFields)
	if client != nil {
		custom("client_cf_", byKey["contact"], clientContact.CustomFields)
	}
	return r, nil
}

// formatCustom writes a custom field value: dates as today_long does, booleans as Yes or
// No, pick lists as the option label.
func formatCustom(d coreCFDef, v any, zone *time.Location) string {
	if v == nil {
		return ""
	}
	switch d.FieldType {
	case "date":
		s, _ := v.(string)
		t, err := time.ParseInLocation(time.DateOnly, s, zone)
		if err != nil {
			return s
		}
		return longDate(t)
	case "boolean":
		if b, ok := v.(bool); ok {
			if b {
				return "Yes"
			}
			return "No"
		}
	case "pick_list":
		s := fmt.Sprint(v)
		for _, o := range d.Options {
			if o == s {
				return o
			}
		}
		return s
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, float64Bits)
	}
	return fmt.Sprint(v)
}

// currentAddress is the contact's primary address with no effective_to; with none
// primary, the open address with the latest effective_from.
func (s *Server) currentAddress(ctx context.Context, tok, contactID string) (coreAddress, error) {
	addrs, err := listAll[coreAddress](ctx, s, "/api/v1/contact-addresses", url.Values{"contact_id": {contactID}}, tok)
	if err != nil {
		return coreAddress{}, err
	}
	var best *coreAddress
	for i := range addrs {
		a := &addrs[i]
		if a.EffectiveTo != nil && *a.EffectiveTo != "" {
			continue
		}
		if a.Primary {
			return *a, nil
		}
		if best == nil || from(a) > from(best) {
			best = a
		}
	}
	if best == nil {
		return coreAddress{}, nil
	}
	return *best, nil
}

func from(a *coreAddress) string {
	if a.EffectiveFrom == nil {
		return ""
	}
	return *a.EffectiveFrom
}

// oneLine writes an address on one line.
func oneLine(a coreAddress) string {
	var parts []string
	for _, p := range []string{a.Line1, a.Line2, a.City} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, strings.TrimSpace(p))
		}
	}
	tail := strings.TrimSpace(strings.TrimSpace(a.Region) + " " + strings.TrimSpace(a.PostalCode))
	if tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, ", ")
}

// Profile is one attorney's signature details.
type Profile struct {
	UserID        string `json:"user_id"`
	BarNumber     string `json:"bar_number"`
	BarState      string `json:"bar_state"`
	CourtesyTitle string `json:"courtesy_title"`
	SignatureLine string `json:"signature_line"`
}

func (s *Server) profile(ctx context.Context, userID string) (Profile, error) {
	p := Profile{UserID: userID}
	err := s.c.DB.QueryRowContext(ctx, `SELECT bar_number, bar_state, courtesy_title, signature_line FROM attorney_profiles WHERE user_id = $1`, userID).
		Scan(&p.BarNumber, &p.BarState, &p.CourtesyTitle, &p.SignatureLine)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}
