package acceptance

import (
	"net/http"
	"time"

	"github.com/CharitonMedia/togavsu/pkg/modulekit/coretest"
)

// world is the spec's synthetic matter on the real core: firm "Example Law LLC",
// matter "Sample v. Placeholder" in Testcounty, client Avery Sample, opposing party
// Riley Placeholder, and coretest's attorney as the responsible attorney.
type world struct {
	Matter, Number, County      string
	Avery, Riley, RileyParty    string
	Firm                        map[string]string
	AttorneyName, AttorneyLast  string
	ClientAddress, ServedAddrID string
}

func (k *kit) id(r coretest.Resp) string {
	k.t.Helper()
	id, _ := r.JSON["id"].(string)
	if id == "" {
		k.t.Fatalf("no id in %d %s", r.Code, r.Body)
	}
	return id
}

func (k *kit) world() world {
	k.t.Helper()
	a := k.Admin
	w := world{Firm: map[string]string{"firm.name": "Example Law LLC", "firm.address": "100 Main St, Testville, MO 65000", "firm.phone": "555-0100",
		"firm.email": "office@example.test", "firm.website": "https://example.test", "firm.time_zone": "America/Chicago"},
		AttorneyName: k.Attorney.Name, AttorneyLast: "Attorney"}
	for key, v := range w.Firm {
		a.Must(http.StatusOK, "PATCH", "/api/v1/settings/"+key, map[string]any{"value": v})
	}
	w.County = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/counties", map[string]any{"name": "Testcounty", "state": "MO"}))
	w.Matter = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/matters", map[string]any{"name": "Sample v. Placeholder", "practice_area_id": k.PracticeArea,
		"billing_type": "hourly", "opened_on": time.Now().UTC().Format(time.DateOnly), "case_number": "CV-26-001", "court": "Circuit Court",
		"county_id": w.County, "client_position": "Petitioner", "responsible_attorney_user_id": k.Attorney.ID}))
	var m struct {
		Number string `json:"number"`
	}
	k.coreJSON(coretest.Admin, "/api/v1/matters/"+w.Matter, &m)
	w.Number = m.Number
	w.Avery = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/contacts", map[string]any{"kind": "person", "given_name": "Avery", "family_name": "Sample"}))
	a.Must(http.StatusCreated, "POST", "/api/v1/contact-addresses", map[string]any{"contact_id": w.Avery, "label": "Home", "primary": true,
		"line1": "1 Test Way", "line2": "Apt 2", "city": "Testville", "region": "MO", "postal_code": "65001"})
	a.Must(http.StatusCreated, "POST", "/api/v1/matter-parties", map[string]any{"matter_id": w.Matter, "contact_id": w.Avery, "role": "client", "primary": true})
	w.Riley = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/contacts", map[string]any{"kind": "person", "given_name": "Riley", "family_name": "Placeholder"}))
	w.ServedAddrID = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/contact-addresses", map[string]any{"contact_id": w.Riley, "label": "Home", "primary": true,
		"line1": "5 Other St", "city": "Testville", "region": "MO", "postal_code": "65002"}))
	w.RileyParty = k.id(a.Must(http.StatusCreated, "POST", "/api/v1/matter-parties", map[string]any{"matter_id": w.Matter, "contact_id": w.Riley,
		"role": "opposing_party", "primary": true, "service_address_id": w.ServedAddrID}))
	return w
}

// needsGeneration is what a generation run reads from the core: library template bodies
// (CORE-17), the firm profile (CORE-18), the user directory (CORE-19) and settings reads
// as the viewer (CORE-20).
func (k *kit) needsGeneration() {
	k.t.Helper()
	k.needs("CORE-17", "CORE-18", "CORE-19", "CORE-20")
}
