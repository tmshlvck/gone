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
	PageSize      int // rows per page; 0 = no pagination
	// Authz is not wired in this iteration; field reserved so the shape
	// matches PRD §6.3 and so callers can store it.

	// Per-instance DOM IDs. Populated by Derive* with a random suffix
	// so multiple CRUDTables can coexist on one page without collisions.
	ListID         string // wraps table + footer; HTMX swap target
	ModalID        string // <dialog> for create/edit forms
	ModalContentID string // inner div the form is swapped into

	Get    func(ctx context.Context, id uint) (T, error)
	List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
	Create func(ctx context.Context, data T) (uint, T, error)
	Update func(ctx context.Context, id uint, data T) (T, error)
	Delete func(ctx context.Context, id uint) error
}

// defaultPageSize is used when CRUDTable.PageSize is 0 and pagination is
// implicitly enabled by the table view (which it always is — set to 0
// only when you intentionally want all rows in one shot).
const defaultPageSize = 20

// DeriveMapCRUDTable wires CRUDTable[T] to a caller-owned map and mutex.
// Search is a case-insensitive substring match against every MetaField
// with Searchable=true. Sort uses reflection on the named field.
//
// The map key is the row's ID. If T has an exported "ID" field of an
// integer kind, Create/Update keep it in sync with the map key.
func DeriveMapCRUDTable[T any](store map[uint]T, mu *sync.RWMutex, mm MetaModel[T]) CRUDTable[T] {
	c := CRUDTable[T]{
		URLBase:        "/" + strings.ToLower(mm.Name),
		MetaData:       mm,
		CreateEnabled:  true,
		EditEnabled:    true,
		DeleteEnabled:  true,
		ListID:         "table_" + randSuffix(),
		ModalID:        "modal_" + randSuffix(),
		ModalContentID: "modal-content_" + randSuffix(),
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
// HTTP wiring. The library only registers PARTIAL endpoints — HTML
// fragments without <html>/<body>/<style> chrome. The application is
// responsible for the main list page (GET {URLBase}) that embeds
// MainComponent inside its own page shell. This keeps the library
// strictly about components and lets the app own page composition.
// ──────────────────────────────────────────────────────────────────────────

// RenderComponent returns the TableView fragment populated from r's
// query parameters (?q, ?sort, ?desc, ?page). Embed it inline in the
// app's page templ for the initial render, OR call it from the app's
// own GET {URLBase} handler to wrap in a page shell.
func (c *CRUDTable[T]) RenderComponent(r *http.Request) (templ.Component, error) {
	d, err := c.buildTableViewData(r)
	if err != nil {
		return nil, err
	}
	return TableView(d), nil
}

// Route registers the CRUD partial endpoints on mux. The main list URL
// (GET {URLBase}) is intentionally NOT registered — apps handle that
// themselves by calling RenderComponent and wrapping in their page shell.
//
// Routes registered (Go 1.22 method+pattern):
//
//	GET    {base}/rows           table fragment for HTMX swaps into #ListID
//	GET    {base}/create         create form fragment (target: ModalContent)
//	POST   {base}/create         submit create
//	GET    {base}/{id}/edit      edit form fragment (target: ModalContent)
//	POST   {base}/{id}/edit      submit update
//	POST   {base}/{id}/delete    delete (HX-Request → rows fragment; else 303)
//	GET    {base}/{id}/display   per-row dump fragment (foundation for
//	                             future extended detail views — today it
//	                             just renders the same fields as the table)
func (c *CRUDTable[T]) Route(mux *http.ServeMux) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	base := c.URLBase
	if base == "" {
		base = "/" + strings.ToLower(c.MetaData.Name)
	}
	c.URLBase = base

	mux.HandleFunc("GET "+base+"/rows", c.makeFragmentHandler(c.handleListRows))
	if c.CreateEnabled {
		mux.HandleFunc("GET "+base+"/create", c.makeFragmentHandler(c.handleCreateForm))
		mux.HandleFunc("POST "+base+"/create", c.makeFragmentHandler(c.handleCreatePost))
	}
	if c.EditEnabled {
		mux.HandleFunc("GET "+base+"/{id}/edit", c.makeFragmentHandler(c.handleEditForm))
		mux.HandleFunc("POST "+base+"/{id}/edit", c.makeFragmentHandler(c.handleEditPost))
	}
	if c.DeleteEnabled {
		mux.HandleFunc("POST "+base+"/{id}/delete", c.makeFragmentHandler(c.handleDeletePost))
	}
	mux.HandleFunc("GET "+base+"/{id}/display", c.makeFragmentHandler(c.handleRowDisplay))
	return nil
}

// handleRowDisplay renders the dump fragment for one row. The Edit
// button (if EditEnabled) hx-gets into the modal-content container so
// the user can transition straight from view → edit.
func (c *CRUDTable[T]) handleRowDisplay(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
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
	d := DumpViewData{}
	if c.EditEnabled {
		idStr := strconv.FormatUint(uint64(id), 10)
		d.EditURL = c.URLBase + "/" + idStr + "/edit"
		d.EditHXTarget = "#" + c.ModalContentID
	}
	return "", c.MetaData.RenderDisplayComponent(row, d)
}

// handlerFunc returns the page title and the page fragment, or sends a
// redirect / error directly and returns ("", nil) to signal "no fragment".
type handlerFunc func(w http.ResponseWriter, r *http.Request) (title string, fragment templ.Component)

// isHTMXRequest reports whether r came from HTMX (so we should respond
// with a partial fragment instead of redirecting).
func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// makeFragmentHandler runs h and writes its returned fragment as the
// response body. The handler may also write the response directly and
// return ("", nil) — e.g. for redirects and errors.
func (c *CRUDTable[T]) makeFragmentHandler(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, frag := h(w, r)
		if frag == nil {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := frag.Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	}
}

// buildTableViewData reads ?q, ?sort, ?desc, ?page from r, queries the
// backend with the right offset/limit, and returns the populated struct.
// Shared by handleList and handleListRows.
func (c *CRUDTable[T]) buildTableViewData(r *http.Request) (TableViewData, error) {
	q := r.URL.Query()
	search := q.Get("q")
	sortBy := q.Get("sort")
	sortDesc := q.Get("desc") == "1"
	page := parsePage(q.Get("page"))

	pageSize := c.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	offset := (page - 1) * pageSize

	results, total, err := c.List(r.Context(), search, sortBy, sortDesc, offset, pageSize)
	if err != nil {
		return TableViewData{}, err
	}
	numPages := 1
	if pageSize > 0 && total > 0 {
		numPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	if page > numPages {
		page = numPages
	}
	rows := make([]TableRow, len(results))
	for i, res := range results {
		rows[i] = TableRow{
			ID:    res.ID,
			Cells: c.MetaData.DisplayValues(c.MetaData, res.Row),
		}
	}
	return TableViewData{
		DisplayName:    c.MetaData.DisplayName,
		URLBase:        c.URLBase,
		Fields:         c.MetaData.Fields,
		Rows:           rows,
		CreateEnabled:  c.CreateEnabled,
		EditEnabled:    c.EditEnabled,
		DeleteEnabled:  c.DeleteEnabled,
		Search:         search,
		SortBy:         sortBy,
		SortDesc:       sortDesc,
		Total:          total,
		Page:           page,
		PageSize:       pageSize,
		NumPages:       numPages,
		ListID:         c.ListID,
		ModalID:        c.ModalID,
		ModalContentID: c.ModalContentID,
	}, nil
}

func parsePage(s string) int {
	if s == "" {
		return 1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func (c *CRUDTable[T]) handleList(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	d, err := c.buildTableViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	return c.MetaData.DisplayName, TableView(d)
}

// handleListRows returns the table + footer for HTMX swaps into
// #crud-list. (Despite the URL ending in /rows, the partial includes
// the table headers and the count/pagination footer so that all of
// those refresh together.)
func (c *CRUDTable[T]) handleListRows(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	d, err := c.buildTableViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	return "", TableContent(d)
}

// createFormView returns the FormView component for a fresh row.
// hxFlow controls HTMX wiring: when true, the form submits via HTMX into
// the table's ListID and we'll close the modal on success via
// HX-Trigger. modelErr renders above the form; fieldErrors render below
// each named field.
//
// The form is rendered via mm.RenderFormComponent with WrapInCard=false
// because the modal-box already provides chrome (no double card).
func (c *CRUDTable[T]) createFormView(modelErr string, fieldErrors map[string]string, data T, hxFlow bool) templ.Component {
	d := FormViewData{
		DisplayName: "Create " + c.MetaData.DisplayName,
		ActionURL:   c.URLBase + "/create",
		BackURL:     c.URLBase,
		SubmitText:  "Create",
		ErrMsg:      modelErr,
		FieldErrors: fieldErrors,
	}
	if hxFlow {
		d.HXTarget = "#" + c.ListID
	}
	return c.MetaData.RenderFormComponent(data, d)
}

// splitValidationErr separates a BindForm error into (perField, modelLevel).
// When err is ValidationErrors:
//   - the entry under ModelLevelKey ("") becomes the modelLevel message
//     and is removed from the per-field map (so it isn't rendered twice).
//   - the remaining entries drive per-field rendering.
// Any other error type becomes a model-level message above the form.
func splitValidationErr(err error) (map[string]string, string) {
	if err == nil {
		return nil, ""
	}
	var verrs ValidationErrors
	if errors.As(err, &verrs) {
		modelErr := verrs[ModelLevelKey]
		fieldErrs := make(map[string]string, len(verrs))
		for k, v := range verrs {
			if k != ModelLevelKey {
				fieldErrs[k] = v
			}
		}
		return fieldErrs, modelErr
	}
	return nil, err.Error()
}

func (c *CRUDTable[T]) handleCreateForm(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	var zero T
	if isHTMXRequest(r) {
		// Pop the modal on the client after the swap lands.
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openModal":%q}`, c.ModalID))
	}
	return "Create " + c.MetaData.DisplayName, c.createFormView("", nil, zero, isHTMXRequest(r))
}

func (c *CRUDTable[T]) handleCreatePost(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil
	}
	var data T
	if err := c.MetaData.BindForm(c.MetaData, r.PostForm, &data); err != nil {
		if isHTMXRequest(r) {
			// Form had hx-target=#crud-list; swap into modal instead so
			// the user sees the error in place.
			w.Header().Set("HX-Retarget", "#"+c.ModalContentID)
			w.Header().Set("HX-Reswap", "innerHTML")
		}
		fieldErrs, modelErr := splitValidationErr(err)
		return "Create " + c.MetaData.DisplayName, c.createFormView(modelErr, fieldErrs, data, isHTMXRequest(r))
	}
	if _, _, err := c.Create(r.Context(), data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	if isHTMXRequest(r) {
		// Return the updated rows fragment; close the modal client-side
		// via the bridged event handler.
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, c.ModalID))
		d, err := c.buildTableViewData(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", nil
		}
		return "", TableContent(d)
	}
	http.Redirect(w, r, c.URLBase, http.StatusSeeOther)
	return "", nil
}

func (c *CRUDTable[T]) editFormView(id uint, modelErr string, fieldErrors map[string]string, row T, hxFlow bool) templ.Component {
	idStr := strconv.FormatUint(uint64(id), 10)
	d := FormViewData{
		DisplayName: "Edit " + c.MetaData.DisplayName + " #" + idStr,
		ActionURL:   c.URLBase + "/" + idStr + "/edit",
		BackURL:     c.URLBase,
		SubmitText:  "Save",
		ErrMsg:      modelErr,
		FieldErrors: fieldErrors,
	}
	if hxFlow {
		d.HXTarget = "#" + c.ListID
	}
	return c.MetaData.RenderFormComponent(row, d)
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
	if isHTMXRequest(r) {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openModal":%q}`, c.ModalID))
	}
	return "Edit " + c.MetaData.DisplayName, c.editFormView(id, "", nil, row, isHTMXRequest(r))
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
		if isHTMXRequest(r) {
			w.Header().Set("HX-Retarget", "#"+c.ModalContentID)
			w.Header().Set("HX-Reswap", "innerHTML")
		}
		fieldErrs, modelErr := splitValidationErr(err)
		return "Edit " + c.MetaData.DisplayName, c.editFormView(id, modelErr, fieldErrs, row, isHTMXRequest(r))
	}
	if _, err := c.Update(r.Context(), id, row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	if isHTMXRequest(r) {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, c.ModalID))
		d, err := c.buildTableViewData(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", nil
		}
		return "", TableContent(d)
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
	// HTMX flow: return the rows fragment so the table re-renders in
	// place without a full page navigation.
	if isHTMXRequest(r) {
		d, err := c.buildTableViewData(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", nil
		}
		return "", TableContent(d)
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
