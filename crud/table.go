package crud

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
)

// CRUDSearchResult bundles a row with the ID the backend assigned to it.
// The ID is exposed separately so handlers don't have to dig into the
// model to discover it.
type CRUDSearchResult[T any] struct {
	ID  uint
	Row T
}

// ErrNotFound is returned by Get/Update/Delete when the id is unknown.
var ErrNotFound = errors.New("not found")

// CRUDTable wraps a MetaModel with the data-plane closures every backend
// must supply. DeriveMapCRUDTable / DeriveGormCRUDTable (later) populate
// the closures; the renderer and Route handlers treat all backends
// uniformly.
type CRUDTable[T any] struct {
	URLBase       string
	MetaData      MetaModel[T]
	CreateEnabled bool
	EditEnabled   bool
	DeleteEnabled bool
	// Authz is not wired in this iteration; field reserved so the shape
	// matches PRD §6.3 and so callers can store it.

	Get    func(ctx context.Context, id uint) (T, error)
	List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
	Create func(ctx context.Context, data T) (uint, T, error)
	Update func(ctx context.Context, id uint, data T) (T, error)
	Delete func(ctx context.Context, id uint) error
}

// DeriveMapCRUDTable wires CRUDTable[T] to a caller-owned map and mutex.
// Search is a case-insensitive substring match against every MetaField
// with Searchable=true. Sort uses reflection on the named field.
//
// The map key is the row's ID. If T has an exported "ID" field of an
// integer kind, Create/Update keep it in sync with the map key.
func DeriveMapCRUDTable[T any](store map[uint]T, mu *sync.RWMutex, mm MetaModel[T]) CRUDTable[T] {
	c := CRUDTable[T]{
		URLBase:       "/" + strings.ToLower(mm.Name),
		MetaData:      mm,
		CreateEnabled: true,
		EditEnabled:   true,
		DeleteEnabled: true,
	}

	searchable := func() []string {
		out := make([]string, 0, len(mm.Fields))
		for _, mf := range mm.Fields {
			if mf.Searchable {
				out = append(out, mf.Name)
			}
		}
		return out
	}()

	c.Get = func(ctx context.Context, id uint) (T, error) {
		mu.RLock()
		defer mu.RUnlock()
		v, ok := store[id]
		if !ok {
			var z T
			return z, ErrNotFound
		}
		return v, nil
	}

	c.List = func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error) {
		mu.RLock()
		defer mu.RUnlock()

		all := make([]CRUDSearchResult[T], 0, len(store))
		for id, v := range store {
			all = append(all, CRUDSearchResult[T]{ID: id, Row: v})
		}

		if search != "" {
			needle := strings.ToLower(search)
			filtered := all[:0]
			for _, r := range all {
				if rowMatchesSearch(r.Row, searchable, needle) {
					filtered = append(filtered, r)
				}
			}
			all = filtered
		}

		if sortBy == "" {
			sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		} else {
			sort.Slice(all, func(i, j int) bool {
				less := compareFieldByName(all[i].Row, all[j].Row, sortBy)
				if sortDesc {
					return !less
				}
				return less
			})
		}

		total := int64(len(all))
		if offset >= len(all) {
			return nil, total, nil
		}
		end := len(all)
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		return all[offset:end], total, nil
	}

	c.Create = func(ctx context.Context, data T) (uint, T, error) {
		mu.Lock()
		defer mu.Unlock()
		id := nextID(store)
		setIDField(&data, id)
		store[id] = data
		return id, data, nil
	}

	c.Update = func(ctx context.Context, id uint, data T) (T, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := store[id]; !ok {
			var z T
			return z, ErrNotFound
		}
		setIDField(&data, id)
		store[id] = data
		return data, nil
	}

	c.Delete = func(ctx context.Context, id uint) error {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := store[id]; !ok {
			return ErrNotFound
		}
		delete(store, id)
		return nil
	}

	return c
}

// ──────────────────────────────────────────────────────────────────────────
// Reflection helpers for map-backend search/sort/ID.
// ──────────────────────────────────────────────────────────────────────────

func rowMatchesSearch[T any](row T, searchFields []string, needle string) bool {
	rv := reflect.ValueOf(row)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return false
	}
	for _, name := range searchFields {
		f := rv.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		if f.Kind() == reflect.String && strings.Contains(strings.ToLower(f.String()), needle) {
			return true
		}
	}
	return false
}

func compareFieldByName[T any](a, b T, field string) bool {
	av := fieldByName(a, field)
	bv := fieldByName(b, field)
	if !av.IsValid() || !bv.IsValid() {
		return false
	}
	return reflectLess(av, bv)
}

func fieldByName(v any, name string) reflect.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return rv.FieldByName(name)
}

func reflectLess(a, b reflect.Value) bool {
	if a.Type() == reflect.TypeOf(time.Time{}) {
		return a.Interface().(time.Time).Before(b.Interface().(time.Time))
	}
	switch a.Kind() {
	case reflect.String:
		return a.String() < b.String()
	case reflect.Bool:
		return !a.Bool() && b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() < b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return a.Uint() < b.Uint()
	case reflect.Float32, reflect.Float64:
		return a.Float() < b.Float()
	}
	return false
}

func nextID[T any](store map[uint]T) uint {
	var n uint = 1
	for id := range store {
		if id >= n {
			n = id + 1
		}
	}
	return n
}

func setIDField[T any](data *T, id uint) {
	rv := reflect.ValueOf(data).Elem()
	if rv.Kind() != reflect.Struct {
		return
	}
	f := rv.FieldByName("ID")
	if !f.IsValid() || !f.CanSet() {
		return
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(uint64(id))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(int64(id))
	}
}

// ──────────────────────────────────────────────────────────────────────────
// HTTP wiring. PageShell lets callers wrap the per-handler fragment in
// their site chrome (head, navigation, footer). If nil, the fragments are
// served raw — fine for testing.
// ──────────────────────────────────────────────────────────────────────────

// PageShell wraps a fragment in a full HTML page. Pass nil to skip
// wrapping (fragment is rendered as the entire response — useful for
// HTMX partial responses; less useful for browser navigation).
type PageShell func(title string, content templ.Component) templ.Component

// Route registers all CRUD routes on mux. Returns an error if mux is nil.
// shell is optional — if nil, fragments render unwrapped.
func (c *CRUDTable[T]) Route(mux *http.ServeMux, shell PageShell) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	base := c.URLBase
	if base == "" {
		base = "/" + strings.ToLower(c.MetaData.Name)
	}
	c.URLBase = base

	mux.HandleFunc("GET "+base, c.makeHandler(shell, c.handleList))
	if c.CreateEnabled {
		mux.HandleFunc("GET "+base+"/create", c.makeHandler(shell, c.handleCreateForm))
		mux.HandleFunc("POST "+base+"/create", c.makeHandler(shell, c.handleCreatePost))
	}
	if c.EditEnabled {
		mux.HandleFunc("GET "+base+"/{id}/edit", c.makeHandler(shell, c.handleEditForm))
		mux.HandleFunc("POST "+base+"/{id}/edit", c.makeHandler(shell, c.handleEditPost))
	}
	if c.DeleteEnabled {
		mux.HandleFunc("POST "+base+"/{id}/delete", c.makeHandler(shell, c.handleDeletePost))
	}
	return nil
}

// handlerFunc returns the page title and the page fragment, or sends a
// redirect / error directly and returns ("", nil) to signal "no fragment".
type handlerFunc func(w http.ResponseWriter, r *http.Request) (title string, fragment templ.Component)

func (c *CRUDTable[T]) makeHandler(shell PageShell, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title, frag := h(w, r)
		if frag == nil {
			return // handler already wrote the response
		}
		var page templ.Component = frag
		if shell != nil {
			page = shell(title, frag)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := page.Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	}
}

func (c *CRUDTable[T]) handleList(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	q := r.URL.Query()
	search := q.Get("q")
	sortBy := q.Get("sort")
	sortDesc := q.Get("desc") == "1"

	results, total, err := c.List(r.Context(), search, sortBy, sortDesc, 0, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	rows := make([]TableRow, len(results))
	for i, res := range results {
		rows[i] = TableRow{
			ID:    res.ID,
			Cells: c.MetaData.DisplayValues(c.MetaData, res.Row),
		}
	}
	return c.MetaData.DisplayName, TableView(TableViewData{
		DisplayName:   c.MetaData.DisplayName,
		URLBase:       c.URLBase,
		Fields:        c.MetaData.Fields,
		Rows:          rows,
		CreateEnabled: c.CreateEnabled,
		EditEnabled:   c.EditEnabled,
		DeleteEnabled: c.DeleteEnabled,
		Search:        search,
		SortBy:        sortBy,
		SortDesc:      sortDesc,
		Total:         total,
	})
}

func (c *CRUDTable[T]) handleCreateForm(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	var zero T
	inputs := c.MetaData.GenFormElements(c.MetaData, zero)
	return "Create " + c.MetaData.DisplayName, FormView(FormViewData{
		DisplayName: "Create " + c.MetaData.DisplayName,
		ActionURL:   c.URLBase + "/create",
		BackURL:     c.URLBase,
		SubmitText:  "Create",
		Fields:      c.MetaData.Fields,
		Inputs:      inputs,
	})
}

func (c *CRUDTable[T]) handleCreatePost(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil
	}
	var data T
	if err := c.MetaData.BindForm(c.MetaData, r.PostForm, &data); err != nil {
		inputs := c.MetaData.GenFormElements(c.MetaData, data)
		return "Create " + c.MetaData.DisplayName, FormView(FormViewData{
			DisplayName: "Create " + c.MetaData.DisplayName,
			ActionURL:   c.URLBase + "/create",
			BackURL:     c.URLBase,
			SubmitText:  "Create",
			Fields:      c.MetaData.Fields,
			Inputs:      inputs,
			ErrMsg:      err.Error(),
		})
	}
	if _, _, err := c.Create(r.Context(), data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	http.Redirect(w, r, c.URLBase, http.StatusSeeOther)
	return "", nil
}

func (c *CRUDTable[T]) handleEditForm(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return "", nil
	}
	row, err := c.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return "", nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	inputs := c.MetaData.GenFormElements(c.MetaData, row)
	idStr := strconv.FormatUint(uint64(id), 10)
	return "Edit " + c.MetaData.DisplayName, FormView(FormViewData{
		DisplayName: "Edit " + c.MetaData.DisplayName + " #" + idStr,
		ActionURL:   c.URLBase + "/" + idStr + "/edit",
		BackURL:     c.URLBase,
		SubmitText:  "Save",
		Fields:      c.MetaData.Fields,
		Inputs:      inputs,
	})
}

func (c *CRUDTable[T]) handleEditPost(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return "", nil
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil
	}
	// Start from the current row so unsubmitted hidden/read-only fields
	// keep their value.
	row, err := c.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return "", nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	if err := c.MetaData.BindForm(c.MetaData, r.PostForm, &row); err != nil {
		inputs := c.MetaData.GenFormElements(c.MetaData, row)
		idStr := strconv.FormatUint(uint64(id), 10)
		return "Edit " + c.MetaData.DisplayName, FormView(FormViewData{
			DisplayName: "Edit " + c.MetaData.DisplayName + " #" + idStr,
			ActionURL:   c.URLBase + "/" + idStr + "/edit",
			BackURL:     c.URLBase,
			SubmitText:  "Save",
			Fields:      c.MetaData.Fields,
			Inputs:      inputs,
			ErrMsg:      err.Error(),
		})
	}
	if _, err := c.Update(r.Context(), id, row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	http.Redirect(w, r, c.URLBase, http.StatusSeeOther)
	return "", nil
}

func (c *CRUDTable[T]) handleDeletePost(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return "", nil
	}
	if err := c.Delete(r.Context(), id); err != nil && !errors.Is(err, ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	http.Redirect(w, r, c.URLBase, http.StatusSeeOther)
	return "", nil
}

func parseID(r *http.Request) (uint, bool) {
	s := r.PathValue("id")
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(n), true
}

// guard against unused imports if the file is trimmed.
var _ = fmt.Sprintf
