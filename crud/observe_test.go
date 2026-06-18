package crud

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/site"
)

// newObservedMapTable builds a map-backed CRUDTable whose Accessor is wrapped
// with ObserveAccessor, returning the mux, the table, and a pointer to the
// captured-events slice (guarded by its own mutex so the test can read it
// after each request).
func newObservedMapTable(t *testing.T, opts ...func(*[]ChangeEvent[item])) (chi.Router, *CRUDTable[item], *[]ChangeEvent[item]) {
	t.Helper()
	store := map[uint]item{
		1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90},
		2: {ID: 2, Name: "Legolas", Realm: "Mirkwood", Power: 85},
	}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})

	var evMu sync.Mutex
	events := make([]ChangeEvent[item], 0)
	on := func(_ context.Context, e ChangeEvent[item]) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
	}

	data := ObserveAccessor(MapAccessor(mm, store, mu), on)
	tbl := NewTable(mm, data, site.DefaultSettings{}, nil)
	mux := chi.NewRouter()
	tbl.RegisterRoutes(mux, "", "/items")
	mux.Get(tbl.URLBase(), func(w http.ResponseWriter, r *http.Request) {
		comp, err := tbl.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = comp.Render(r.Context(), w)
	})
	return mux, &tbl, &events
}

func TestObserve_CreateFiresEvent(t *testing.T) {
	mux, _, events := newObservedMapTable(t)
	rec := postForm(t, mux, "/items/create", "Name=Frodo&Realm=Shire&Power=42")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(*events) != 1 {
		t.Fatalf("want 1 event, got %d", len(*events))
	}
	e := (*events)[0]
	if e.Kind != ChangeCreate {
		t.Errorf("kind = %v, want create", e.Kind)
	}
	if e.ID == 0 {
		t.Error("event ID should be the new row's assigned id, got 0")
	}
	if e.Row.Name != "Frodo" {
		t.Errorf("event Row.Name = %q, want Frodo", e.Row.Name)
	}
}

func TestObserve_UpdateFiresEvent(t *testing.T) {
	mux, _, events := newObservedMapTable(t)
	rec := postForm(t, mux, "/items/1/edit", "Name=Strider&Realm=Gondor&Power=91")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(*events) != 1 {
		t.Fatalf("want 1 event, got %d", len(*events))
	}
	e := (*events)[0]
	if e.Kind != ChangeUpdate || e.ID != 1 {
		t.Errorf("got %v id=%d, want update id=1", e.Kind, e.ID)
	}
	if e.Row.Name != "Strider" {
		t.Errorf("event Row.Name = %q, want Strider", e.Row.Name)
	}
}

func TestObserve_DeleteFiresEvent(t *testing.T) {
	mux, _, events := newObservedMapTable(t)
	rec := postForm(t, mux, "/items/2/delete", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(*events) != 1 {
		t.Fatalf("want 1 event, got %d", len(*events))
	}
	e := (*events)[0]
	if e.Kind != ChangeDelete || e.ID != 2 {
		t.Errorf("got %v id=%d, want delete id=2", e.Kind, e.ID)
	}
	// Plain ObserveAccessor doesn't re-read on delete: Row is the zero value.
	if e.Row.Name != "" {
		t.Errorf("plain delete event should carry zero Row, got %+v", e.Row)
	}
}

func TestObserveDeletes_CarriesOldRow(t *testing.T) {
	store := map[uint]item{1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90}}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})

	var got ChangeEvent[item]
	var seen int
	data := ObserveDeletes(MapAccessor(mm, store, mu), func(_ context.Context, e ChangeEvent[item]) {
		got = e
		seen++
	})
	if err := data.Delete(context.Background(), 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if seen != 1 {
		t.Fatalf("want 1 event, got %d", seen)
	}
	if got.Kind != ChangeDelete || got.ID != 1 || got.Row.Name != "Aragorn" {
		t.Errorf("event = %+v, want delete id=1 Row.Name=Aragorn", got)
	}
}

func TestObserveReads_GetAndListFireEvents(t *testing.T) {
	store := map[uint]item{
		1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90},
		2: {ID: 2, Name: "Legolas", Realm: "Mirkwood", Power: 85},
	}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})

	var events []ChangeEvent[item]
	data := ObserveReads(MapAccessor(mm, store, mu), func(_ context.Context, e ChangeEvent[item]) {
		events = append(events, e)
	})

	// Get → one ChangeRead carrying the row.
	if _, err := data.Get(context.Background(), 1); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(events) != 1 || events[0].Kind != ChangeRead || events[0].ID != 1 || events[0].Row.Name != "Aragorn" {
		t.Fatalf("after Get, events = %+v", events)
	}

	// List → one ChangeList carrying the returned-row count (no per-row ids).
	events = nil
	if _, _, err := data.List(context.Background(), "", "", false, 0, 10); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 || events[0].Kind != ChangeList || events[0].Count != 2 {
		t.Fatalf("after List, events = %+v", events)
	}
}

// TestObserveAccessor_ReadsSilent confirms the plain (write-only) wrap does NOT
// emit on Get/List — reads stay quiet unless you opt in with ObserveReads.
func TestObserveAccessor_ReadsSilent(t *testing.T) {
	store := map[uint]item{1: {ID: 1, Name: "Aragorn"}}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})

	var seen int
	data := ObserveAccessor(MapAccessor(mm, store, mu), func(context.Context, ChangeEvent[item]) { seen++ })
	_, _ = data.Get(context.Background(), 1)
	_, _, _ = data.List(context.Background(), "", "", false, 0, 10)
	if seen != 0 {
		t.Errorf("plain ObserveAccessor fired %d read events, want 0", seen)
	}
}

// TestObserveReads_FailedReadDoesNotFire ensures a Get/List error suppresses the
// read event, mirroring the write path.
func TestObserveReads_FailedReadDoesNotFire(t *testing.T) {
	var seen int
	data := ObserveReads[item](failingAccessor[item]{}, func(context.Context, ChangeEvent[item]) { seen++ })
	if _, err := data.Get(context.Background(), 1); err == nil {
		t.Fatal("expected Get error")
	}
	if _, _, err := data.List(context.Background(), "", "", false, 0, 10); err == nil {
		t.Fatal("expected List error")
	}
	if seen != 0 {
		t.Errorf("failed reads fired %d events, want 0", seen)
	}
}

// TestObserve_FailedMutationDoesNotFire ensures a backend error suppresses the
// event — observers only see committed writes.
func TestObserve_FailedMutationDoesNotFire(t *testing.T) {
	var seen int
	data := ObserveAccessor[item](failingAccessor[item]{}, func(context.Context, ChangeEvent[item]) {
		seen++
	})
	if _, _, err := data.Create(context.Background(), item{}); err == nil {
		t.Fatal("expected Create error")
	}
	if _, err := data.Update(context.Background(), 1, item{}); err == nil {
		t.Fatal("expected Update error")
	}
	if err := data.Delete(context.Background(), 1); err == nil {
		t.Fatal("expected Delete error")
	}
	if seen != 0 {
		t.Errorf("failed mutations fired %d events, want 0", seen)
	}
}

// TestObserve_NilCallbackIsTransparent guards the nil-callback degradation:
// reads and writes still work, nothing panics.
func TestObserve_NilCallbackIsTransparent(t *testing.T) {
	store := map[uint]item{}
	mu := &sync.RWMutex{}
	mm := DeriveMetaModel[item](MetaModel[item]{})
	data := ObserveAccessor(MapAccessor(mm, store, mu), nil)
	id, _, err := data.Create(context.Background(), item{Name: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := data.Get(context.Background(), id); err != nil {
		t.Errorf("Get: %v", err)
	}
}

// TestObserve_CSVImportFiresEvents proves the wrap catches the CSV import path,
// which calls Data.Create/Update directly rather than going through the
// create/edit handlers.
func TestObserve_CSVImportFiresEvents(t *testing.T) {
	mux, _, events := newObservedMapTable(t)
	// One new row (blank ID → create) and one existing (ID=1 → update).
	csv := "ID,Name,Realm,Power\n,Frodo,Shire,42\n1,Strider,Gondor,91\n"
	rec := postForm(t, mux, "/items/import", "csv="+urlEscape(csv))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if len(*events) != 2 {
		t.Fatalf("want 2 import events, got %d: %+v", len(*events), *events)
	}
	var creates, updates int
	for _, e := range *events {
		switch e.Kind {
		case ChangeCreate:
			creates++
		case ChangeUpdate:
			updates++
		}
	}
	if creates != 1 || updates != 1 {
		t.Errorf("import events: creates=%d updates=%d, want 1 each", creates, updates)
	}
}

// failingAccessor is an Accessor[T] whose mutations always error, for asserting
// that observers don't fire on failure.
type failingAccessor[T any] struct{}

var errBackend = errors.New("backend boom")

func (failingAccessor[T]) Get(context.Context, uint) (T, error) {
	var z T
	return z, errBackend
}
func (failingAccessor[T]) List(context.Context, string, string, bool, int, int) ([]CRUDSearchResult[T], int64, error) {
	return nil, 0, errBackend
}
func (failingAccessor[T]) Create(context.Context, T) (uint, T, error) {
	var z T
	return 0, z, errBackend
}
func (failingAccessor[T]) Update(context.Context, uint, T) (T, error) {
	var z T
	return z, errBackend
}
func (failingAccessor[T]) Delete(context.Context, uint) error { return errBackend }
