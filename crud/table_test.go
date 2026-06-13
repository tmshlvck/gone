package crud

import (
	"context"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type item struct {
	ID    uint
	Name  string
	Realm string
	Power int
}

func newTestServer(t *testing.T) (chi.Router, *CRUDTable[item]) {
	t.Helper()
	store := map[uint]item{
		1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90},
		2: {ID: 2, Name: "Legolas", Realm: "Mirkwood", Power: 85},
		3: {ID: 3, Name: "Gandalf", Realm: "Middle-earth", Power: 99},
		4: {ID: 4, Name: "Boromir", Realm: "Gondor", Power: 70},
	}
	mu := &sync.RWMutex{}
	mm, err := DeriveMetaModel[item]()
	if err != nil {
		t.Fatalf("DeriveMetaModel: %v", err)
	}
	tbl := DeriveMapCRUDTable[item](mm, nil, store, mu)
	tbl.Slug = "items"
	mux := chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "")
	// CRUDTable.Route registers only partial endpoints. The "main" page
	// route is the app's job — for tests we register a thin handler
	// that just renders Render as a bare fragment (no page
	// shell, since the tests only inspect HTML structure, not chrome).
	mux.Get(tbl.URLBase(), func(w http.ResponseWriter, r *http.Request) {
		comp, err := tbl.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = comp.Render(r.Context(), w)
	})
	return mux, &tbl
}

func get(t *testing.T, mux chi.Router, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func postForm(t *testing.T, mux chi.Router, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestList_ShowsAllRowsByDefault(t *testing.T) {
	mux, tbl := newTestServer(t)
	code, body := get(t, mux, "/items")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, name := range []string{"Aragorn", "Legolas", "Gandalf", "Boromir"} {
		if !strings.Contains(body, name) {
			t.Errorf("missing %q in list", name)
		}
	}
	// The list-area wrapper carries the per-instance id derived at
	// Derive time — assert the id is present and follows the
	// "table_<rand>" format.
	if !strings.Contains(body, `id="`+tbl.ListID+`"`) {
		t.Errorf("table should have id=%q wrapper for HTMX swaps", tbl.ListID)
	}
	if !strings.HasPrefix(tbl.ListID, "table_") {
		t.Errorf("ListID = %q; want prefix 'table_'", tbl.ListID)
	}
}

func TestList_TableViewHasHTMXAttrs(t *testing.T) {
	mux, tbl := newTestServer(t)
	_, body := get(t, mux, "/items")
	for _, tok := range []string{
		`hx-get="/items/view`,
		`hx-target="#` + tbl.ListID + `"`,
		`hx-push-url`,
	} {
		if !strings.Contains(body, tok) {
			t.Errorf("expected %q in TableView output", tok)
		}
	}
}

func TestViewPartial_IsFragmentNotFullPage(t *testing.T) {
	mux, _ := newTestServer(t)
	code, body := get(t, mux, "/items/view")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	// /view is a fragment that lands inside #crud-list, so it has the
	// <table> + footer but never the outer page chrome.
	for _, forbidden := range []string{"<html", "<head", "<body", "card-body"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/view must not emit %q; got: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "<table") {
		t.Errorf("/view should contain <table>; got: %s", body)
	}
	if !strings.Contains(body, "Aragorn") {
		t.Errorf("/view missing data rows")
	}
	if !strings.Contains(body, "row(s)") {
		t.Errorf("/view should include the row-count footer")
	}
}

func TestList_Search(t *testing.T) {
	mux, _ := newTestServer(t)
	_, body := get(t, mux, "/items?q=gondor")
	if !strings.Contains(body, "Aragorn") || !strings.Contains(body, "Boromir") {
		t.Errorf("Gondor heroes should be present")
	}
	if strings.Contains(body, "Legolas") || strings.Contains(body, "Gandalf") {
		t.Errorf("non-Gondor heroes should be filtered out")
	}
}

func TestList_SortAsc(t *testing.T) {
	mux, _ := newTestServer(t)
	_, body := get(t, mux, "/items?sort=Power")
	// Ascending: Boromir 70, Legolas 85, Aragorn 90, Gandalf 99
	idxBoromir := strings.Index(body, "Boromir")
	idxLegolas := strings.Index(body, "Legolas")
	idxAragorn := strings.Index(body, "Aragorn")
	idxGandalf := strings.Index(body, "Gandalf")
	if !(idxBoromir < idxLegolas && idxLegolas < idxAragorn && idxAragorn < idxGandalf) {
		t.Errorf("ascending sort wrong: B=%d L=%d A=%d G=%d",
			idxBoromir, idxLegolas, idxAragorn, idxGandalf)
	}
}

func TestList_SortDesc(t *testing.T) {
	mux, _ := newTestServer(t)
	_, body := get(t, mux, "/items?sort=Power&desc=1")
	idxBoromir := strings.Index(body, "Boromir")
	idxLegolas := strings.Index(body, "Legolas")
	idxAragorn := strings.Index(body, "Aragorn")
	idxGandalf := strings.Index(body, "Gandalf")
	if !(idxGandalf < idxAragorn && idxAragorn < idxLegolas && idxLegolas < idxBoromir) {
		t.Errorf("descending sort wrong: G=%d A=%d L=%d B=%d",
			idxGandalf, idxAragorn, idxLegolas, idxBoromir)
	}
}

func TestCreate_PostRedirectsAndPersists(t *testing.T) {
	mux, tbl := newTestServer(t)
	rec := postForm(t, mux, "/items/create",
		"Name=Frodo&Realm=Shire&Power=42")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/items" {
		t.Errorf("Location = %q", loc)
	}
	rows, total, err := tbl.List(context.Background(), "Frodo", "", false, 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || rows[0].Row.Name != "Frodo" {
		t.Errorf("Frodo not persisted, got total=%d rows=%+v", total, rows)
	}
	if rows[0].Row.ID == 0 {
		t.Error("ID field should be set on created row")
	}
}

func TestUpdate_PostMutates(t *testing.T) {
	mux, tbl := newTestServer(t)
	rec := postForm(t, mux, "/items/4/edit",
		"Name=Boromir&Realm=Gondor&Power=88")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", rec.Code)
	}
	row, err := tbl.Get(context.Background(), 4)
	if err != nil {
		t.Fatalf("Get(4): %v", err)
	}
	if row.Power != 88 {
		t.Errorf("Power = %d, want 88", row.Power)
	}
}

func TestDelete_BrowserRequestRedirects(t *testing.T) {
	mux, tbl := newTestServer(t)
	rec := postForm(t, mux, "/items/2/delete", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("non-HTMX delete: status %d, want 303", rec.Code)
	}
	if _, err := tbl.Get(context.Background(), 2); err == nil {
		t.Error("id 2 should be gone")
	}
}

func TestDelete_HTMXRequestReturnsRowsFragment(t *testing.T) {
	mux, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/items/2/delete", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTMX delete: status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// HTMX delete returns the list fragment (table + footer) but never
	// the page chrome.
	for _, forbidden := range []string{"<html", "<head", "<body"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("HTMX delete must not return page chrome %q; got: %s", forbidden, body)
		}
	}
	if strings.Contains(body, "Legolas") {
		t.Errorf("Legolas should have been deleted from the response: %s", body)
	}
	if !strings.Contains(body, "Aragorn") {
		t.Errorf("remaining rows should still be present: %s", body)
	}
}

func TestCreate_BindErrorReRendersForm(t *testing.T) {
	mux, _ := newTestServer(t)
	rec := postForm(t, mux, "/items/create",
		"Name=X&Realm=Y&Power=notnumber")
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("bad input should not redirect")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alert-error") && !strings.Contains(body, "Power") {
		t.Errorf("expected error rendering, got: %s", body)
	}
	// Field-only errors should still produce the summary banner so the
	// user can't miss them when they're below the fold.
	if !strings.Contains(body, "Please correct the errors below.") {
		t.Errorf("expected field-error summary banner, got: %s", body)
	}
}

func TestRowDisplayPartial(t *testing.T) {
	mux, _ := newTestServer(t)
	code, body := get(t, mux, "/items/1/display")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "Aragorn") {
		t.Errorf("expected Aragorn in /1/display body: %s", body)
	}
	if !strings.Contains(body, "Gondor") {
		t.Errorf("expected Gondor in /1/display body: %s", body)
	}
	// The display fragment is barebone — just the data table, no
	// card/Edit button. Chrome is the caller's job.
	for _, forbidden := range []string{"<html", "<body", "card-body", `hx-get="/items/1/edit"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/display fragment must not include %q", forbidden)
		}
	}
	if !strings.Contains(body, "<table") {
		t.Errorf("/display should contain the data <table>: %s", body)
	}
}

func TestRowDisplay_NotFound(t *testing.T) {
	mux, _ := newTestServer(t)
	code, _ := get(t, mux, "/items/999/display")
	if code != http.StatusNotFound {
		t.Errorf("expected 404 for missing id, got %d", code)
	}
}

func TestDeriveMapCRUDTable_DefaultSlugFromName(t *testing.T) {
	mm, _ := DeriveMetaModel[item]()
	tbl := DeriveMapCRUDTable[item](mm, nil, map[uint]item{}, &sync.RWMutex{})
	if tbl.Slug != "items" {
		t.Errorf("default Slug = %q, want items", tbl.Slug)
	}
}

// ──────────────────────────────────────────────────────────────────
// Authz-driven button states + page retention across mutations.

// readOnly is a stub auth.Authz that says "you may list and read but
// never mutate". Used to test that the CRUDTable hides / disables
// mutation buttons based on the Can* answers.
type readOnly struct{}

func (readOnly) CanList(*http.Request) bool   { return true }
func (readOnly) CanRead(*http.Request) bool   { return true }
func (readOnly) CanCreate(*http.Request) bool { return false }
func (readOnly) CanUpdate(*http.Request) bool { return false }
func (readOnly) CanDelete(*http.Request) bool { return false }

func newTestServerWithAuthz(t *testing.T, az authzFunc, hideUnauthorized bool) chi.Router {
	t.Helper()
	store := map[uint]item{
		1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90},
		2: {ID: 2, Name: "Legolas", Realm: "Mirkwood", Power: 85},
		3: {ID: 3, Name: "Gandalf", Realm: "Middle-earth", Power: 99},
		4: {ID: 4, Name: "Boromir", Realm: "Gondor", Power: 70},
	}
	mu := &sync.RWMutex{}
	mm, _ := DeriveMetaModel[item]()
	tbl := DeriveMapCRUDTable[item](mm, az, store, mu)
	tbl.Slug = "items"
	tbl.HideUnauthorized = hideUnauthorized
	mux := chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "")
	mux.Get(tbl.URLBase(), func(w http.ResponseWriter, r *http.Request) {
		comp, _ := tbl.Render(r)
		_ = comp.Render(r.Context(), w)
	})
	return mux
}

// authzFunc is a tiny stand-in for auth.Authz that wraps a closure.
// Cheaper than defining a method-y type per test.
type authzFunc struct {
	list, read, create, update, del bool
}

func (a authzFunc) CanList(*http.Request) bool   { return a.list }
func (a authzFunc) CanRead(*http.Request) bool   { return a.read }
func (a authzFunc) CanCreate(*http.Request) bool { return a.create }
func (a authzFunc) CanUpdate(*http.Request) bool { return a.update }
func (a authzFunc) CanDelete(*http.Request) bool { return a.del }

func TestHideUnauthorizedTrue_OmitsButtons(t *testing.T) {
	mux := newTestServerWithAuthz(t, authzFunc{list: true, read: true}, true)
	_, body := get(t, mux, "/items")
	if strings.Contains(body, "+ Create") {
		t.Errorf("Create button rendered despite HideUnauthorized=true: %s", body)
	}
	if strings.Contains(body, ">edit<") || strings.Contains(body, ">delete<") {
		t.Error("edit/delete buttons rendered despite HideUnauthorized=true")
	}
}

func TestHideUnauthorizedFalse_RendersDisabled(t *testing.T) {
	mux := newTestServerWithAuthz(t, authzFunc{list: true, read: true}, false)
	_, body := get(t, mux, "/items")
	// Buttons appear but with the btn-disabled class + disabled attribute,
	// and crucially without hx-get (so a click can't fire a request).
	if !strings.Contains(body, "+ Create") {
		t.Errorf("Create button missing despite HideUnauthorized=false: %s", body)
	}
	if !strings.Contains(body, "btn-disabled") {
		t.Errorf("disabled buttons should carry btn-disabled class: %s", body)
	}
	// Spot-check: the Create button's section should NOT contain hx-get
	// (the disabled variant is plain).
	createIdx := strings.Index(body, "+ Create")
	if createIdx < 0 {
		t.Fatal("no Create button found")
	}
	// Walk back to the opening <button … and verify no hx-get inside.
	start := strings.LastIndex(body[:createIdx], "<button")
	if start < 0 {
		t.Fatal("no <button opening tag before Create")
	}
	end := strings.Index(body[start:], ">")
	btnAttrs := body[start : start+end]
	if strings.Contains(btnAttrs, "hx-get") {
		t.Errorf("disabled Create button must not have hx-get: %s", btnAttrs)
	}
}

func TestAuthorizedRendersClickable(t *testing.T) {
	mux := newTestServerWithAuthz(t,
		authzFunc{list: true, read: true, create: true, update: true, del: true},
		false)
	_, body := get(t, mux, "/items")
	if !strings.Contains(body, "+ Create") || !strings.Contains(body, "hx-get=\"/items/create\"") {
		t.Errorf("create button should be clickable when CanCreate; body: %s", body)
	}
	if !strings.Contains(body, ">edit<") || !strings.Contains(body, "/edit\"") {
		t.Errorf("edit button should be clickable when CanUpdate")
	}
}

// TestMutationRetainsPage: when an HTMX POST lands on a mutation
// endpoint and the user was looking at e.g. page=2, the refresh
// should re-render page 2 — not snap back to page 1. CRUDTable reads
// HX-Current-URL for that context.
func TestMutationRetainsPage(t *testing.T) {
	mux := newTestServerWithAuthz(t,
		authzFunc{list: true, read: true, create: true, update: true, del: true},
		false)

	// Pad the store so we can paginate. Reusing the testServer fixture
	// already has 4 rows — set PageSize=2 by inserting via /create
	// would be slow; instead reach in: this test re-creates the table
	// with PageSize=2 from scratch.
	store := map[uint]item{}
	for i := 1; i <= 6; i++ {
		store[uint(i)] = item{ID: uint(i), Name: "Hero" + string(rune('A'+i-1))}
	}
	mu := &sync.RWMutex{}
	mm, _ := DeriveMetaModel[item]()
	tbl := DeriveMapCRUDTable[item](mm, nil, store, mu)
	tbl.Slug = "items"
	tbl.PageSize = 2
	mux = chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "")

	// Sanity: page=2 lists rows 3-4.
	code, body := get(t, mux, "/items/view?page=2")
	if code != 200 {
		t.Fatalf("page=2 status = %d", code)
	}
	if !strings.Contains(body, "HeroC") || !strings.Contains(body, "HeroD") {
		t.Errorf("page=2 should list HeroC/HeroD; got: %s", body)
	}

	// Delete a row while the user is on page 2. The mutation request
	// URL is /items/N/delete (no page param), but HX-Current-URL on
	// the page is /items?page=2. The refreshed fragment should
	// re-render page 2 — i.e. the rows that follow the deleted one.
	req := httptest.NewRequest("POST", "/items/3/delete", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://example.com/items?page=2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete status = %d, body: %s", rec.Code, rec.Body.String())
	}
	// HeroD (id 4) was on page 2; should still be there after id 3 left.
	// After delete: rows 1,2,4,5,6 — page 2 of pageSize 2 = rows 4,5.
	body = rec.Body.String()
	if !strings.Contains(body, "HeroD") || !strings.Contains(body, "HeroE") {
		t.Errorf("post-delete page=2 should list HeroD/HeroE; got: %s", body)
	}
	if strings.Contains(body, "HeroA") || strings.Contains(body, "HeroB") {
		t.Errorf("post-delete page=2 should NOT list HeroA/HeroB; got: %s", body)
	}
}

// TestMutationClampsBeyondLastPage: when a delete empties the last
// page, the refresh should clamp to the new last page rather than
// rendering an empty out-of-range page.
func TestMutationClampsBeyondLastPage(t *testing.T) {
	store := map[uint]item{
		1: {ID: 1, Name: "HeroA"},
		2: {ID: 2, Name: "HeroB"},
		3: {ID: 3, Name: "HeroC"}, // sole row on page 2
	}
	mu := &sync.RWMutex{}
	mm, _ := DeriveMetaModel[item]()
	tbl := DeriveMapCRUDTable[item](mm, nil, store, mu)
	tbl.Slug = "items"
	tbl.PageSize = 2
	mux := chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "")

	req := httptest.NewRequest("POST", "/items/3/delete", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://example.com/items?page=2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete status = %d", rec.Code)
	}
	body := rec.Body.String()
	// After delete: page=2 doesn't exist any more (only 2 rows total).
	// buildTableViewData clamps page to the new last page = 1, which
	// holds HeroA + HeroB.
	if !strings.Contains(body, "HeroA") || !strings.Contains(body, "HeroB") {
		t.Errorf("clamped refresh should list HeroA/HeroB; got: %s", body)
	}
}

// Suppress unused-warning for readOnly — kept exported above for symmetry
// with the auth.Authz interface but unused by current tests.
var _ = readOnly{}
