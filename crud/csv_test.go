package crud

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/site"
)

// TestCSVExport_StreamsAllRows checks the export endpoint emits a header (ID
// first, no duplicate ID column) plus one row per record, honoring the search
// filter.
func TestCSVExport_StreamsAllRows(t *testing.T) {
	mux, _ := newTestServer(t)

	code, body := get(t, mux, "/items/export.csv")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if got := strings.TrimSpace(lines[0]); got != "ID,Name,Realm,Power" {
		t.Fatalf("header = %q, want \"ID,Name,Realm,Power\"", got)
	}
	if len(lines) != 5 { // header + 4 rows
		t.Fatalf("got %d lines, want 5:\n%s", len(lines), body)
	}
	for _, name := range []string{"Aragorn", "Legolas", "Gandalf", "Boromir"} {
		if !strings.Contains(body, name) {
			t.Errorf("export missing %q", name)
		}
	}

	// Filtered export only carries matching rows.
	code, body = get(t, mux, "/items/export.csv?q=Gondor")
	if code != 200 {
		t.Fatalf("filtered status %d", code)
	}
	if !strings.Contains(body, "Aragorn") || !strings.Contains(body, "Boromir") {
		t.Errorf("filtered export should keep Gondor rows:\n%s", body)
	}
	if strings.Contains(body, "Legolas") {
		t.Errorf("filtered export leaked a non-matching row:\n%s", body)
	}
}

// postCSVFile builds a multipart request with the CSV in the "file" part, the
// way the import form's file input submits.
func postCSVFile(t *testing.T, mux http.Handler, path, csv string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "import.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(csv)); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestCSVImport_CreatesAndUpdates verifies upsert: a blank-ID row creates, an
// ID-bearing row updates in place.
func TestCSVImport_CreatesAndUpdates(t *testing.T) {
	mux, tbl := newTestServer(t)

	csv := "ID,Name,Realm,Power\n" +
		",Frodo,Shire,40\n" + // create
		"1,Aragorn,Gondor,95\n" // update #1's Power 90 → 95

	rec := postCSVFile(t, mux, "/items/import", csv)
	if rec.Code != 200 {
		t.Fatalf("import status %d: %s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	updated, err := tbl.Data.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Power != 95 {
		t.Errorf("row 1 Power = %d, want 95 (update)", updated.Power)
	}

	_, total, err := tbl.Data.List(ctx, "", "", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 { // 4 seed + 1 created
		t.Errorf("row count = %d, want 5 after import", total)
	}
	results, _, _ := tbl.Data.List(ctx, "Frodo", "", false, 0, 0)
	if len(results) != 1 {
		t.Fatalf("Frodo not created (found %d)", len(results))
	}
}

// TestCSVImport_RejectsBadRowAtomically confirms a single bad row rejects the
// whole file: nothing is created or updated.
func TestCSVImport_RejectsBadRowAtomically(t *testing.T) {
	mux, tbl := newTestServer(t)

	csv := "ID,Name,Realm,Power\n" +
		",Frodo,Shire,40\n" + // would create
		",Sam,Shire,notanumber\n" // bad Power → parse error

	rec := postCSVFile(t, mux, "/items/import", csv)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Import rejected") {
		t.Errorf("expected rejection notice, got:\n%s", rec.Body.String())
	}

	_, total, _ := tbl.Data.List(context.Background(), "", "", false, 0, 0)
	if total != 4 {
		t.Errorf("row count = %d, want 4 (nothing imported)", total)
	}
}

// TestCSVImport_TextareaPaste exercises the urlencoded textarea path (no file).
func TestCSVImport_TextareaPaste(t *testing.T) {
	mux, tbl := newTestServer(t)

	form := "csv=" + urlEncode("ID,Name,Realm,Power\n,Bilbo,Shire,30\n")
	req := httptest.NewRequest("POST", "/items/import", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	results, _, _ := tbl.Data.List(context.Background(), "Bilbo", "", false, 0, 0)
	if len(results) != 1 {
		t.Fatalf("Bilbo not created from textarea (found %d)", len(results))
	}
}

func urlEncode(s string) string {
	r := strings.NewReplacer("\n", "%0A", ",", "%2C", " ", "+")
	return r.Replace(s)
}

// csvSelRel is a relation target for the field-selection test below.
type csvSelRel struct {
	ID   uint
	Name string
}

// csvSelModel exercises every field category the CSV selectors care about:
// scalar, read-only, secret, hidden FK, and all three relation kinds.
type csvSelModel struct {
	ID       uint
	Title    string
	Created  string      // marked ReadOnly via preset
	Secret   string      // marked NoExport via preset
	Owner    csvSelRel   `gorm:"foreignKey:OwnerID"` // RelationSingle
	OwnerID  uint        // sibling FK → auto-Hidden by derive
	Tags     []csvSelRel `gorm:"many2many:csv_tags"`  // RelationMany2Many
	Children []csvSelRel `gorm:"foreignKey:ParentID"` // RelationHasMany (read-only)
}

func fieldNames(fs []MetaField) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Name] = true
	}
	return m
}

// TestCSV_FieldSelection pins down which fields export vs import include,
// across scalars, read-only, secret, hidden, and the three relation kinds.
func TestCSV_FieldSelection(t *testing.T) {
	mm := DeriveMetaModel[csvSelModel](MetaModel[csvSelModel]{
		Fields: []MetaField{
			{Name: "Created", ReadOnly: true},
			{Name: "Secret", NoExport: true},
		},
	})

	exp := fieldNames(csvExportFields(mm))
	imp := fieldNames(csvImportFields(mm))

	// Export: every non-secret, non-internal column — incl. read-only and
	// the has-many inverse; excl. ID, the hidden FK scalar, and the secret.
	wantExp := map[string]bool{"Title": true, "Created": true, "Owner": true, "Tags": true, "Children": true}
	if !sameSet(exp, wantExp) {
		t.Errorf("export fields = %v, want %v", keys(exp), keys(wantExp))
	}

	// Import: writable columns only — excl. ID, hidden FK, read-only Created,
	// and the read-only has-many Children; secret stays importable.
	wantImp := map[string]bool{"Title": true, "Secret": true, "Owner": true, "Tags": true}
	if !sameSet(imp, wantImp) {
		t.Errorf("import fields = %v, want %v", keys(imp), keys(wantImp))
	}
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCSVImport_PartialColumnsArePatch confirms a CSV with a subset of columns
// updates only those fields and leaves the rest of the row intact (PATCH
// semantics) rather than wiping omitted columns to zero.
func TestCSVImport_PartialColumnsArePatch(t *testing.T) {
	mux, tbl := newTestServer(t)

	rec := postCSVFile(t, mux, "/items/import", "ID,Power\n1,77\n")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := tbl.Data.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Power != 77 {
		t.Errorf("Power = %d, want 77 (patched)", got.Power)
	}
	if got.Name != "Aragorn" || got.Realm != "Gondor" {
		t.Errorf("omitted columns wiped: Name=%q Realm=%q, want Aragorn/Gondor", got.Name, got.Realm)
	}
}

// TestCSV_NoExportExcludesButImports verifies a NoExport field is dropped from
// export yet still settable on import — and that a blank cell leaves it
// untouched.
func TestCSV_NoExportExcludesButImports(t *testing.T) {
	store := map[uint]item{1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90}}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})
	// Mark Realm NoExport for the purposes of this test (stand-in for a secret).
	f, err := mm.FindField("Realm")
	if err != nil {
		t.Fatal(err)
	}
	f.NoExport = true
	tbl := NewTable(mm, MapAccessor(mm, store, mu), site.DefaultSettings{}, nil)
	mux := chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "/items")

	// Export omits the NoExport column.
	_, body := get(t, mux, "/items/export.csv")
	header := strings.SplitN(body, "\n", 2)[0]
	if strings.Contains(header, "Realm") {
		t.Errorf("export header leaked NoExport field: %q", strings.TrimSpace(header))
	}

	// Import can still set it when a value is given...
	rec := postCSVFile(t, mux, "/items/import", "ID,Realm\n1,Arnor\n")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := tbl.Data.Get(context.Background(), 1)
	if got.Realm != "Arnor" {
		t.Errorf("NoExport field not imported: Realm=%q, want Arnor", got.Realm)
	}

	// ...but a blank cell leaves it unchanged (no wipe).
	rec = postCSVFile(t, mux, "/items/import", "ID,Realm\n1,\n")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	got, _ = tbl.Data.Get(context.Background(), 1)
	if got.Realm != "Arnor" {
		t.Errorf("blank NoExport cell wiped the field: Realm=%q, want Arnor", got.Realm)
	}
}

// TestCSV_Many2ManyRoundTrip exercises the M2M id-list cell: export emits a
// semicolon-separated id list, and import re-binds it.
func TestCSV_Many2ManyRoundTrip(t *testing.T) {
	mux, htbl, _, _ := newGormServer(t)
	ctx := context.Background()

	// Export: Aragorn (#1) seeded with skill #1 → "Skills" column holds "1".
	_, body := get(t, mux, "/heroes/export.csv")
	if !strings.Contains(body, "Skills") {
		t.Fatalf("export missing Skills column:\n%s", body)
	}

	// Import: give Aragorn both skills via the id list.
	rec := postCSVFile(t, mux, "/heroes/import", "ID,Skills\n1,\"1;2\"\n")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := htbl.Data.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("Skills = %d, want 2 after import", len(got.Skills))
	}

	// A blank list clears the set.
	rec = postCSVFile(t, mux, "/heroes/import", "ID,Skills\n1,\n")
	if rec.Code != 200 {
		t.Fatalf("clear status %d", rec.Code)
	}
	got, _ = htbl.Data.Get(ctx, 1)
	if len(got.Skills) != 0 {
		t.Errorf("Skills = %d, want 0 after blank import", len(got.Skills))
	}
}
