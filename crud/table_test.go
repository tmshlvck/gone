package crud

import (
	"context"
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

func newTestServer(t *testing.T) (*http.ServeMux, *CRUDTable[item]) {
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
	tbl := DeriveMapCRUDTable[item](store, mu, mm)
	tbl.URLBase = "/items"
	mux := http.NewServeMux()
	if err := tbl.Route(mux, nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	return mux, &tbl
}

func get(t *testing.T, mux *http.ServeMux, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func postForm(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestList_ShowsAllRowsByDefault(t *testing.T) {
	mux, _ := newTestServer(t)
	code, body := get(t, mux, "/items")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, name := range []string{"Aragorn", "Legolas", "Gandalf", "Boromir"} {
		if !strings.Contains(body, name) {
			t.Errorf("missing %q in list", name)
		}
	}
	if !strings.Contains(body, `id="crud-list"`) {
		t.Error("table should have id=\"crud-list\" wrapper for HTMX swaps")
	}
}

func TestList_TableViewHasHTMXAttrs(t *testing.T) {
	mux, _ := newTestServer(t)
	_, body := get(t, mux, "/items")
	for _, tok := range []string{
		`hx-get="/items/rows`,
		`hx-target="#crud-list"`,
		`hx-push-url`,
	} {
		if !strings.Contains(body, tok) {
			t.Errorf("expected %q in TableView output", tok)
		}
	}
}

func TestRowsPartial_IsFragmentNotFullPage(t *testing.T) {
	mux, _ := newTestServer(t)
	code, body := get(t, mux, "/items/rows")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	// /rows is a fragment that lands inside #crud-list, so it has the
	// <table> + footer but never the outer page chrome.
	for _, forbidden := range []string{"<html", "<head", "<body", "card-body"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/rows must not emit %q; got: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "<table") {
		t.Errorf("/rows should contain <table>; got: %s", body)
	}
	if !strings.Contains(body, "Aragorn") {
		t.Errorf("/rows missing data rows")
	}
	if !strings.Contains(body, "row(s)") {
		t.Errorf("/rows should include the row-count footer")
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
}

func TestDeriveMapCRUDTable_DefaultURLBaseFromName(t *testing.T) {
	mm, _ := DeriveMetaModel[item]()
	tbl := DeriveMapCRUDTable[item](map[uint]item{}, &sync.RWMutex{}, mm)
	if tbl.URLBase != "/item" {
		t.Errorf("default URLBase = %q, want /item", tbl.URLBase)
	}
}
