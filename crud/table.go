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
// must supply. DeriveMapCRUDTable / DeriveGormCRUDTable populate the
// closures; the renderer and Route handlers treat all backends uniformly.
//
// urlBase is set by Route(mux, prefix) to prefix + "/" + Slug — read it
// via HTMXTableURL / HTMXCreateURL. Slug defaults to a lowercased plural
// of MetaData.Name; override before Route for irregular plurals.
type CRUDTable[T any] struct {
	MetaData      MetaModel[T]
	Authz         AuthzInterface // nil = AllowAll
	Slug          string         // url-safe plural; default = lowercase(Name) + "s"
	PageSize      int            // rows per page; 0 = library default (20)
	CreateEnabled bool
	EditEnabled   bool
	DeleteEnabled bool

	// urlBase becomes prefix + "/" + Slug once Route is called. Private
	// because external readers always go through HTMXTableURL /
	// HTMXCreateURL — the field name shouldn't be a stable API.
	urlBase string

	// ListID wraps the table + footer; HTMX swap target for list
	// refreshes. Per-instance random suffix so multiple CRUDTables
	// can coexist on one page without collision.
	ListID string

	Get    func(ctx context.Context, id uint) (T, error)
	List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
	Create func(ctx context.Context, data T) (uint, T, error)
	Update func(ctx context.Context, id uint, data T) (T, error)
	Delete func(ctx context.Context, id uint) error
}

// defaultSlug returns a heuristic plural for a Go type name. Wrong for
// irregular plurals (Hero→heros, Person→persons, Sheep→sheeps) — caller
// overrides CRUDTable.Slug for those.
func defaultSlug(name string) string {
	return strings.ToLower(name) + "s"
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
//
// authz gates every route Route() registers (nil = AllowAll).
func DeriveMapCRUDTable[T any](mm MetaModel[T], authz AuthzInterface, store map[uint]T, mu *sync.RWMutex) CRUDTable[T] {
	c := CRUDTable[T]{
		MetaData:      mm,
		Authz:         authz,
		Slug:          defaultSlug(mm.Name),
		CreateEnabled: true,
		EditEnabled:   true,
		DeleteEnabled: true,
		ListID:        "table_" + randSuffix(),
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

// Route registers the CRUD partial endpoints on mux under
// prefix + "/" + Slug. The main page URL (GET {urlBase}) is
// intentionally NOT registered — apps handle that themselves by
// calling Render and wrapping in their page shell.
//
// Routes registered (Go 1.22 method+pattern):
//
//	GET    {urlBase}/view              table fragment for HTMX swaps into #ListID
//	GET    {urlBase}/create            create form fragment (target: modal body)
//	POST   {urlBase}/create            submit create
//	GET    {urlBase}/{id}/edit         edit form fragment (target: modal body)
//	POST   {urlBase}/{id}/edit         submit update
//	POST   {urlBase}/{id}/delete       delete (HX-Request → rows fragment; else 303)
//	GET    {urlBase}/{id}/display      per-row barebone dump fragment
//	GET    {urlBase}/options           relation-picker option list
//
// Every handler gates on c.Authz (CanList / CanRead / CanCreate /
// CanUpdate / CanDelete); nil = AllowAll.
//
// For chi-based callers wanting middleware layering: use chi.Group
// (which stacks middleware without changing the prefix). chi.Route
// would prefix-mount and double the absolute paths registered here.
func (c *CRUDTable[T]) Route(mux Mux, prefix string) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	if c.Slug == "" {
		c.Slug = defaultSlug(c.MetaData.Name)
	}
	c.urlBase = prefix + "/" + strings.TrimPrefix(c.Slug, "/")
	base := c.urlBase

	mux.HandleFunc("GET "+base+"/view", c.makeFragmentHandler(c.handleListRows, "list"))
	if c.CreateEnabled {
		mux.HandleFunc("GET "+base+"/create", c.makeFragmentHandler(c.handleCreateForm, "create"))
		mux.HandleFunc("POST "+base+"/create", c.makeFragmentHandler(c.handleCreatePost, "create"))
	}
	if c.EditEnabled {
		mux.HandleFunc("GET "+base+"/{id}/edit", c.makeFragmentHandler(c.handleEditForm, "read"))
		mux.HandleFunc("POST "+base+"/{id}/edit", c.makeFragmentHandler(c.handleEditPost, "update"))
	}
	if c.DeleteEnabled {
		mux.HandleFunc("POST "+base+"/{id}/delete", c.makeFragmentHandler(c.handleDeletePost, "delete"))
	}
	mux.HandleFunc("GET "+base+"/{id}/display", c.makeFragmentHandler(c.handleRowDisplay, "read"))
	// Relation picker option fetch — used by another CRUD's relation
	// widget when its <select> needs to refresh after an L2 save.
	mux.HandleFunc("GET "+base+"/options", c.makeFragmentHandler(c.handleOptions, "list"))
	return nil
}

// handleRowDisplay renders the barebone dump fragment for one row. No
// Edit button — the caller wraps the fragment with whatever chrome they
// want (e.g. a /heroes/{id} detail page that adds Edit / Back links).
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
	return "", c.MetaData.RenderDisplay(row)
}

// handlerFunc returns the page title and the page fragment, or sends a
// redirect / error directly and returns ("", nil) to signal "no fragment".
type handlerFunc func(w http.ResponseWriter, r *http.Request) (title string, fragment templ.Component)


// authzGate returns true (and lets the handler run) when the requesting
// user is allowed to perform the named action. Denials send 403 and
// return false. action ∈ {"list","read","create","update","delete"}.
func (c *CRUDTable[T]) authzGate(w http.ResponseWriter, r *http.Request, action string) bool {
	authz := authzOrAllow(c.Authz)
	var ok bool
	switch action {
	case "list":
		ok = authz.CanList(r)
	case "read":
		ok = authz.CanRead(r)
	case "create":
		ok = authz.CanCreate(r)
	case "update":
		ok = authz.CanUpdate(r)
	case "delete":
		ok = authz.CanDelete(r)
	default:
		ok = false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	return ok
}

// makeFragmentHandler runs h (after the authz gate) and writes its
// returned fragment as the response body. The handler may also write
// the response directly and return ("", nil) — e.g. for redirects and
// errors.
func (c *CRUDTable[T]) makeFragmentHandler(h handlerFunc, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.authzGate(w, r, action) {
			return
		}
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
		DisplayName:   c.MetaData.DisplayName,
		URLBase:       c.urlBase,
		Fields:        c.MetaData.Fields,
		Rows:          rows,
		CreateEnabled: c.CreateEnabled,
		EditEnabled:   c.EditEnabled,
		DeleteEnabled: c.DeleteEnabled,
		Search:        search,
		SortBy:        sortBy,
		SortDesc:      sortDesc,
		Total:         total,
		Page:          page,
		PageSize:      pageSize,
		NumPages:      numPages,
		ListID:        c.ListID,
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
// bodyID names the modal body the form is being rendered into — the
// form's hx-target points back to it so validation errors re-render
// in place. When bodyID is "" the form has no HTMX wiring (browser
// fallback). modelErr renders above the form; fieldErrors render
// below each named field.
//
// FormView is barebone — the modal-box that surrounds it provides
// chrome. CRUDTable builds FormViewData directly (rather than going
// through mm.RenderFormComponent) because the URLs are per-action
// (/create, /{id}/edit) and don't match mm.FormURL.
func (c *CRUDTable[T]) createFormView(errs ValidationErrors, data T, bodyID string) templ.Component {
	d := FormViewData{
		DisplayName: "Create " + c.MetaData.DisplayName,
		ActionURL:   c.urlBase + "/create",
		SubmitText:  "Create",
		Fields:      c.MetaData.Fields,
		Inputs:      c.MetaData.GenFormElements(c.MetaData, data),
		Errors:      errs,
	}
	if bodyID != "" {
		d.HXTarget = "#" + bodyID
	}
	return FormView(d)
}

func (c *CRUDTable[T]) handleCreateForm(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	var zero T
	modalID, bodyID := modalIDsFromHeader(r)
	if isHTMXRequest(r) {
		// Pop the right modal (L1 for the table's own form; L2 if this
		// GET came from a relation widget's "+ new" button).
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openModal":%q}`, modalID))
	}
	formBodyID := ""
	if isHTMXRequest(r) {
		formBodyID = bodyID
	}
	return "Create " + c.MetaData.DisplayName, c.createFormView(nil, zero, formBodyID)
}

func (c *CRUDTable[T]) handleCreatePost(w http.ResponseWriter, r *http.Request) (string, templ.Component) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", nil
	}
	modalID, bodyID := modalIDsFromHeader(r)
	var data T
	if err := c.MetaData.BindForm(c.MetaData, r.PostForm, &data); err != nil {
		if isHTMXRequest(r) {
			// Validation failure: re-render the form in the same modal
			// body it came from.
			w.Header().Set("HX-Retarget", "#"+bodyID)
			w.Header().Set("HX-Reswap", "innerHTML")
		}
		formBodyID := ""
		if isHTMXRequest(r) {
			formBodyID = bodyID
		}
		return "Create " + c.MetaData.DisplayName, c.createFormView(ValidationErrorsFromError(err), data, formBodyID)
	}
	if _, _, err := c.Create(r.Context(), data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	if isHTMXRequest(r) {
		if modalID == ModalL2ID {
			// Nested L2 create (from a relation "+" button) — close L2,
			// don't touch L1's form values. The refresh-relation event
			// makes every L1 relation widget re-fetch its <option>
			// list so the freshly-created row appears in the dropdown.
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q,"refresh-relation":true}`, modalID))
			w.Header().Set("HX-Reswap", "none")
			return "", nil
		}
		// L1 success: redirect the swap from the modal body to the
		// table's list area and return the refreshed rows.
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, modalID))
		w.Header().Set("HX-Retarget", "#"+c.ListID)
		w.Header().Set("HX-Reswap", "innerHTML")
		d, err := c.buildTableViewData(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", nil
		}
		return "", TableContent(d)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return "", nil
}

func (c *CRUDTable[T]) editFormView(id uint, errs ValidationErrors, row T, bodyID string) templ.Component {
	idStr := strconv.FormatUint(uint64(id), 10)
	d := FormViewData{
		DisplayName: "Edit " + c.MetaData.DisplayName + " #" + idStr,
		ActionURL:   c.urlBase + "/" + idStr + "/edit",
		SubmitText:  "Save",
		Fields:      c.MetaData.Fields,
		Inputs:      c.MetaData.GenFormElements(c.MetaData, row),
		Errors:      errs,
	}
	if bodyID != "" {
		d.HXTarget = "#" + bodyID
	}
	return FormView(d)
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
	modalID, bodyID := modalIDsFromHeader(r)
	if isHTMXRequest(r) {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openModal":%q}`, modalID))
	}
	formBodyID := ""
	if isHTMXRequest(r) {
		formBodyID = bodyID
	}
	return "Edit " + c.MetaData.DisplayName, c.editFormView(id, nil, row, formBodyID)
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
	modalID, bodyID := modalIDsFromHeader(r)
	if err := c.MetaData.BindForm(c.MetaData, r.PostForm, &row); err != nil {
		if isHTMXRequest(r) {
			w.Header().Set("HX-Retarget", "#"+bodyID)
			w.Header().Set("HX-Reswap", "innerHTML")
		}
		formBodyID := ""
		if isHTMXRequest(r) {
			formBodyID = bodyID
		}
		return "Edit " + c.MetaData.DisplayName, c.editFormView(id, ValidationErrorsFromError(err), row, formBodyID)
	}
	if _, err := c.Update(r.Context(), id, row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", nil
	}
	if isHTMXRequest(r) {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, modalID))
		if modalID == ModalL2ID {
			// Unlikely (no UI path opens edit in L2 today) but be safe.
			w.Header().Set("HX-Reswap", "none")
			return "", nil
		}
		w.Header().Set("HX-Retarget", "#"+c.ListID)
		w.Header().Set("HX-Reswap", "innerHTML")
		d, err := c.buildTableViewData(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", nil
		}
		return "", TableContent(d)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
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
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return "", nil
}

