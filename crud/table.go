package crud

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/auth"
	"github.com/tmshlvck/gone/htmx"
	"github.com/tmshlvck/gone/site"
)

// confirmDeletePrefName is the per-browser preference cookie that gates the
// per-row delete confirmation dialog. Absent/empty = confirm (the safe
// default); the literal "off" suppresses hx-confirm so deletes fire on the
// first click. Toggled from the table's ⋮ menu (see confirmDeleteToggleScript).
const confirmDeletePrefName = "gone_crud_confirm_delete"

// CRUDTable pairs a MetaModel (how to render/bind) with an Accessor (the
// data plane) plus table-level config. The renderer and route handlers are
// backend-blind — they go through Data.
//
// Construction says WHAT (metadata + data + behaviour); RegisterRoutes says
// WHERE (the path namespace). Build a table with NewTable, then mount it:
//
//	mm    := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{DisplayName: "Heroes"})
//	data  := crud.GORMAccessor[Hero](mm, db)
//	table := crud.NewTable(mm, data, site.PageSize(10), az)
//	table.RegisterRoutes(root, "", "/admin/heroes")
type CRUDTable[T any] struct {
	MetaData MetaModel[T]
	Authz    auth.Authz // nil = AuthzAllowAll
	// Pagination supplies the rows-per-page (PaginationSizeDefault): a value
	// of 0 means no pagination — show every matching row. nil → 20 (the
	// site.DefaultSettings default). Pass the app's site.DefaultSettings for
	// the default, or site.PageSize(n) for a specific size.
	Pagination    site.PaginationSettings
	CreateEnabled bool
	EditEnabled   bool
	DeleteEnabled bool

	// HideUnauthorized controls how mutation buttons render when Authz
	// denies them.
	//   false (default): the buttons render as visually disabled — they
	//     stay in the DOM so the UI shape stays stable across users and
	//     it's obvious the feature exists.
	//   true: the buttons are omitted entirely. Cleaner UI for read-only
	//     users; harder for a casual visitor to tell a feature even
	//     exists.
	// Independent of CreateEnabled/EditEnabled/DeleteEnabled: a button
	// only shows if the *Enabled toggle says so AND (HideUnauthorized
	// is false OR Authz permits the action).
	HideUnauthorized bool

	// Data is the data plane — Get/List/Create/Update/Delete. Built by a
	// backend constructor (MapAccessor / GORMAccessor) or any custom Accessor.
	Data Accessor[T]

	// ShortLabel overrides DefaultShortLabel for this model — the short label
	// shown for one of its rows in a relation <select> option (this table's
	// /options endpoint) and in a relation cell on another table (wired by
	// WireRelations). nil uses DefaultShortLabel.
	ShortLabel func(T) string

	// urlBase becomes routerPrefix + componentPath once RegisterRoutes is
	// called. Private because external readers go through URLBase().
	urlBase string

	// componentPath is where the table is mounted RELATIVE to its router
	// (e.g. "/admin/heroes"). Set by RegisterRoutes.
	componentPath string

	// modalKey is a DOM-id-safe key derived from componentPath; it namespaces
	// this table's L1 modal so multiple tables coexist on one page.
	modalKey string

	// ListID wraps the table + footer; HTMX swap target for list
	// refreshes. Per-instance random suffix so multiple CRUDTables
	// can coexist on one page without collision.
	ListID string
}

// defaultSlug returns a heuristic plural for a Go type name. Wrong for
// irregular plurals (Hero→heros, Person→persons, Sheep→sheeps) — pass an
// explicit componentPath to RegisterRoutes for those.
func defaultSlug(name string) string {
	return strings.ToLower(name) + "s"
}

// pathKey turns a (possibly multi-segment) component path into a DOM-id-safe
// key: "/admin/heroes" → "admin-heroes", "" / "/" → "root".
func pathKey(p string) string {
	p = strings.Trim(p, "/")
	p = strings.ReplaceAll(p, "/", "-")
	if p == "" {
		return "root"
	}
	return p
}

// NewTable pairs a MetaModel with an Accessor (the data plane) into a ready
// CRUDTable. pager supplies the page size: pass the app's site.DefaultSettings
// for the default (20/page), site.PageSize(n) for a specific size, or
// site.PageSize(0) for no pagination (all rows). nil also means the default.
// authz gates every route (nil = allow all). Create/Edit/Delete are enabled by
// default — toggle the *Enabled fields, HideUnauthorized, or ShortLabel on the
// returned value before RegisterRoutes.
//
// The data Accessor must be built from the SAME mm (GORMAccessor/MapAccessor
// read mm to learn which fields are searchable/sortable/relations).
func NewTable[T any](mm MetaModel[T], data Accessor[T], pager site.PaginationSettings, authz auth.Authz) CRUDTable[T] {
	return CRUDTable[T]{
		MetaData:      mm,
		Data:          data,
		Authz:         authz,
		Pagination:    pager,
		CreateEnabled: true,
		EditEnabled:   true,
		DeleteEnabled: true,
		ListID:        "table_" + randSuffix(),
	}
}

// ──────────────────────────────────────────────────────────────────────────
// HTTP wiring. The library only registers PARTIAL endpoints — HTML
// fragments without <html>/<body>/<style> chrome. The application is
// responsible for the main list page (GET {URLBase}) that embeds
// MainComponent inside its own page shell. This keeps the library
// strictly about components and lets the app own page composition.
// ──────────────────────────────────────────────────────────────────────────

// Render returns the TableView fragment populated from r's query
// parameters (?q, ?sort, ?desc, ?page) plus this table's own L1 modal
// dialog. Embed it inline in the app's page templ for the initial
// render, or call it from the app's own page handler to wrap in a
// page shell.
func (c *CRUDTable[T]) Render(r *http.Request) (templ.Component, error) {
	d, err := c.buildTableViewData(r)
	if err != nil {
		return nil, err
	}
	return TableView(d), nil
}

// RegisterRoutes mounts the table's in-component (fragment) endpoints on r.
// It does NOT register a whole-page handler — the application owns the page
// route and embeds Render(r) in its own chrome.
//
// Two strings place the table — construction said WHAT, this says WHERE:
//
//   - routerPrefix is the ABSOLUTE path at which r itself is served (the
//     caller knows it; chi can't report it at registration time). "" when r
//     is the root mux.
//   - componentPath is where this table sits RELATIVE to r — one or more
//     segments, e.g. "/heroes" or "/admin/heroes". Empty falls back to a
//     derived plural of the model name (Hero→"heros"; pass an explicit path
//     for irregular plurals). The table's absolute base, used for every
//     rendered hx-get / form action, is normalizePrefix(routerPrefix) +
//     componentPath.
//
// Routes are registered relative to r, so the table composes on the root mux
// without a stripping chi.Route. For componentPath="/admin/heroes",
// routerPrefix="":
//
//	GET    /admin/heroes/view          table fragment for HTMX swaps into #ListID
//	GET    /admin/heroes/create        create form fragment (target: modal body)
//	POST   /admin/heroes/create        submit create
//	GET    /admin/heroes/{id}/edit     edit form fragment (target: modal body)
//	POST   /admin/heroes/{id}/edit     submit update
//	POST   /admin/heroes/{id}/delete   delete (HX-Request → rows fragment; else 303)
//	GET    /admin/heroes/{id}/display  per-row barebone dump fragment
//	GET    /admin/heroes/options       relation-picker option list
//
// Every handler gates on c.Authz (CanList / CanRead / CanCreate /
// CanUpdate / CanDelete); nil = AllowAll.
func (c *CRUDTable[T]) RegisterRoutes(r chi.Router, routerPrefix, componentPath string) {
	if componentPath == "" {
		componentPath = defaultSlug(c.MetaData.Name)
	}
	rel := "/" + strings.Trim(componentPath, "/")
	c.componentPath = rel
	c.modalKey = pathKey(rel)
	c.urlBase = normalizePrefix(routerPrefix) + rel

	r.Get(rel+"/view", c.makeFragmentHandler(c.handleListRows, "list"))
	if c.CreateEnabled {
		r.Get(rel+"/create", c.makeFragmentHandler(c.handleCreateForm, "create"))
		r.Post(rel+"/create", c.makeFragmentHandler(c.handleCreatePost, "create"))
	}
	if c.EditEnabled {
		r.Get(rel+"/{id}/edit", c.makeFragmentHandler(c.handleEditForm, "read"))
		r.Post(rel+"/{id}/edit", c.makeFragmentHandler(c.handleEditPost, "update"))
	}
	if c.DeleteEnabled {
		r.Post(rel+"/{id}/delete", c.makeFragmentHandler(c.handleDeletePost, "delete"))
	}
	// CSV export rides the list authz; it's a full file response, not a
	// fragment, so it bypasses makeFragmentHandler.
	r.Get(rel+"/export.csv", c.handleExportCSV)
	// CSV import upserts rows, so it's offered whenever this table can create
	// or edit; the gate (importAllowed) accepts either capability.
	if c.CreateEnabled || c.EditEnabled {
		r.Get(rel+"/import", c.makeImportHandler(c.handleImportForm))
		r.Post(rel+"/import", c.makeImportHandler(c.handleImportPost))
	}
	r.Get(rel+"/{id}/display", c.makeFragmentHandler(c.handleRowDisplay, "read"))
	// Relation picker option fetch — used by another CRUD's relation
	// widget when its <select> needs to refresh after an L2 save.
	r.Get(rel+"/options", c.makeFragmentHandler(c.handleOptions, "list"))
}

// handleRowDisplay renders the barebone dump fragment for one row. No
// Edit button — the caller wraps the fragment with whatever chrome they
// want (e.g. a /heroes/{id} detail page that adds Edit / Back links).
func (c *CRUDTable[T]) handleRowDisplay(w http.ResponseWriter, r *http.Request) templ.Component {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil
	}
	row, err := c.Data.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	if failInternal(w, err) {
		return nil
	}
	return c.MetaData.RenderDisplay(row)
}

// handlerFunc returns the fragment to write, or nil to signal the
// handler already sent the response itself (redirect / error / etc).
type handlerFunc func(w http.ResponseWriter, r *http.Request) templ.Component

// authzGate returns true (and lets the handler run) when the requesting
// user is allowed to perform the named action. Denials send 403 and
// return false. action ∈ {"list","read","create","update","delete"}.
func (c *CRUDTable[T]) authzGate(w http.ResponseWriter, r *http.Request, action string) bool {
	az := auth.AuthzOrAllow(c.Authz)
	var ok bool
	switch action {
	case "list":
		ok = az.CanList(r)
	case "read":
		ok = az.CanRead(r)
	case "create":
		ok = az.CanCreate(r)
	case "update":
		ok = az.CanUpdate(r)
	case "delete":
		ok = az.CanDelete(r)
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
		frag := h(w, r)
		if frag == nil {
			return
		}
		site.Fragment(w, r, frag)
	}
}

// buildTableViewData reads ?q, ?sort, ?desc, ?page from r, queries the
// backend with the right offset/limit, and returns the populated struct.
// Shared by handleList and handleListRows.
//
// For HTMX requests that came from a mutation handler (Create/Edit/
// Delete POST) r.URL points at the mutation endpoint and carries no
// list state. listQueryFor falls back to HX-Current-URL — the URL of
// the page the user was looking at — so a delete on page 3 refreshes
// page 3 instead of snapping back to page 1.
func (c *CRUDTable[T]) buildTableViewData(r *http.Request) (TableViewData, error) {
	q := listQueryFor(r)
	search := q.Get("q")
	sortBy := q.Get("sort")
	sortDesc := q.Get("desc") == "1"
	page := parsePage(q.Get("page"))

	// Effective rows-per-page from the table's PaginationSettings: 0 means no
	// pagination (the accessor treats limit<=0 as "all rows"); nil settings
	// fall back to the library default.
	pager := c.Pagination
	if pager == nil {
		pager = site.DefaultSettings{}
	}
	pageSize := int(pager.PaginationSizeDefault())
	offset := (page - 1) * pageSize

	results, total, err := c.Data.List(r.Context(), search, sortBy, sortDesc, offset, pageSize)
	if err != nil {
		return TableViewData{}, err
	}
	numPages := 1
	if pageSize > 0 && total > 0 {
		numPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	if page > numPages {
		// The user asked for a page past the end — e.g. after a delete
		// emptied the last page. Clamp and re-query so the response
		// actually contains the last valid page's rows instead of an
		// empty tbody.
		page = numPages
		offset = (page - 1) * pageSize
		if offset < 0 {
			offset = 0
		}
		results, _, err = c.Data.List(r.Context(), search, sortBy, sortDesc, offset, pageSize)
		if err != nil {
			return TableViewData{}, err
		}
	}
	rows := make([]TableRow, len(results))
	for i, res := range results {
		rows[i] = TableRow{
			ID:    res.ID,
			Cells: c.MetaData.DisplayValues(res.Row),
		}
	}
	az := auth.AuthzOrAllow(c.Authz)
	return TableViewData{
		DisplayName:      c.MetaData.DisplayName,
		URLBase:          c.urlBase,
		Fields:           c.MetaData.Fields,
		Rows:             rows,
		CreateEnabled:    c.CreateEnabled,
		EditEnabled:      c.EditEnabled,
		DeleteEnabled:    c.DeleteEnabled,
		CanCreate:        az.CanCreate(r),
		CanUpdate:        az.CanUpdate(r),
		CanDelete:        az.CanDelete(r),
		CanImport:        c.importAllowed(r),
		ConfirmDelete:    site.Pref(r, confirmDeletePrefName) != "off",
		HideUnauthorized: c.HideUnauthorized,
		Search:           search,
		SortBy:           sortBy,
		SortDesc:         sortDesc,
		Total:            total,
		Page:             page,
		PageSize:         pageSize,
		NumPages:         numPages,
		ListID:           c.ListID,
		L1ModalID:        L1ModalIDFromSlug(c.modalKey),
		L1BodyID:         L1BodyIDFromSlug(c.modalKey),
	}, nil
}

// listQueryFor returns the query params that describe the current list
// state. For list GETs (the /view sort/search/paginate links, and the
// page route itself) r.URL IS the desired state — the link encodes it in
// full, including "no params" meaning "sort off" — so we read r.URL.
// HX-Current-URL must NOT be consulted here: it holds the PRE-click URL
// (hx-push-url runs only after the swap), so trusting it lags the table one
// click behind (the classic "sort arrow appears a click late" bug).
//
// POST mutations are different: their r.URL is the mutation endpoint
// (/create, /{id}/edit, /{id}/delete) and carries no list state, so we
// recover the page/sort/search the user was looking at from HX-Current-URL —
// that keeps a delete on page 3 refreshing page 3.
func listQueryFor(r *http.Request) url.Values {
	if r.Method == http.MethodGet {
		return r.URL.Query()
	}
	if u, ok := htmx.CurrentURL(r); ok {
		return u.Query()
	}
	return r.URL.Query()
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

// handleListRows returns the table + footer for HTMX swaps into the
// ListID wrapper — table headers, body rows, and the count/pagination
// footer all refresh together.
func (c *CRUDTable[T]) handleListRows(w http.ResponseWriter, r *http.Request) templ.Component {
	d, err := c.buildTableViewData(r)
	if failInternal(w, err) {
		return nil
	}
	return TableContent(d)
}

// createForm / editForm render the create / edit form for the table by
// reusing the MetaModel's RenderForm primitive (the same one applications
// call when they own the routing) — the table only supplies the per-action
// URL and label. The form's hx-target is the level-agnostic modalFormTarget
// (closest .crud-modal-body), so the same markup re-renders in place on a
// validation error whether it's shown in the L1 or the shared L2 modal.
func (c *CRUDTable[T]) createForm(errs ValidationErrors, data T) templ.Component {
	return c.MetaData.RenderForm(data, FormOpts{
		Title:       "Create " + c.MetaData.DisplayName,
		ActionURL:   c.urlBase + "/create",
		SubmitLabel: "Create",
		Errors:      errs,
		HXTarget:    modalFormTarget,
	})
}

func (c *CRUDTable[T]) editForm(id uint, errs ValidationErrors, row T) templ.Component {
	idStr := strconv.FormatUint(uint64(id), 10)
	return c.MetaData.RenderForm(row, FormOpts{
		Title:       "Edit " + c.MetaData.DisplayName + " #" + idStr,
		ActionURL:   c.urlBase + "/" + idStr + "/edit",
		SubmitLabel: "Save",
		Errors:      errs,
		HXTarget:    modalFormTarget,
	})
}

// afterMutation finishes a successful HTMX create or edit: it closes the modal
// the form lived in and refreshes the page. The two levels differ in WHAT to
// refresh, not in how the modal closes (the client always closes the topmost):
//
//   - Nested (L2) create — opened from a relation "+ new" button — runs on the
//     *related* table's handler, whose list area isn't on the current page. So
//     it swaps nothing and just broadcasts refresh-relation, making the parent
//     form's <select>s reload so the new row appears.
//   - Normal (L1) create/edit swaps the refreshed table into its own list area.
func (c *CRUDTable[T]) afterMutation(w http.ResponseWriter, r *http.Request) templ.Component {
	reply := htmx.Reply().Trigger(crudCloseModalEvent, nil)
	if isNestedModal(r) {
		reply.Trigger(refreshRelationEvent, true).Reswap("none").Apply(w)
		return nil
	}
	d, err := c.buildTableViewData(r)
	if failInternal(w, err) {
		return nil
	}
	reply.Retarget("#" + c.ListID).Reswap("innerHTML").Apply(w)
	return TableContent(d)
}

func (c *CRUDTable[T]) handleCreateForm(w http.ResponseWriter, r *http.Request) templ.Component {
	var zero T
	return c.createForm(nil, zero) // the client opens the modal on swap
}

func (c *CRUDTable[T]) handleCreatePost(w http.ResponseWriter, r *http.Request) templ.Component {
	var data T
	if err := c.MetaData.TryBindForm(r, &data); err != nil {
		// Validation failure: the form re-renders into its own modal body via
		// its hx-target — no server retarget needed, modal stays open.
		return c.createForm(ValidationErrorsFromError(err), data)
	}
	if _, _, err := c.Data.Create(r.Context(), data); failInternal(w, err) {
		return nil
	}
	if htmx.IsRequest(r) {
		return c.afterMutation(w, r)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return nil
}

func (c *CRUDTable[T]) handleEditForm(w http.ResponseWriter, r *http.Request) templ.Component {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil
	}
	row, err := c.Data.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	if failInternal(w, err) {
		return nil
	}
	return c.editForm(id, nil, row) // the client opens the modal on swap
}

func (c *CRUDTable[T]) handleEditPost(w http.ResponseWriter, r *http.Request) templ.Component {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil
	}
	// Start from the current row so unsubmitted hidden/read-only fields
	// keep their value.
	row, err := c.Data.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	if failInternal(w, err) {
		return nil
	}
	if err := c.MetaData.TryBindForm(r, &row); err != nil {
		return c.editForm(id, ValidationErrorsFromError(err), row)
	}
	if _, err := c.Data.Update(r.Context(), id, row); failInternal(w, err) {
		return nil
	}
	if htmx.IsRequest(r) {
		return c.afterMutation(w, r)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return nil
}

func (c *CRUDTable[T]) handleDeletePost(w http.ResponseWriter, r *http.Request) templ.Component {
	id, ok := parseID(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil
	}
	if err := c.Data.Delete(r.Context(), id); err != nil && !errors.Is(err, ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	// HTMX flow: return the rows fragment so the table re-renders in
	// place without a full page navigation.
	if htmx.IsRequest(r) {
		d, err := c.buildTableViewData(r)
		if failInternal(w, err) {
			return nil
		}
		return TableContent(d)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// CSV export / import.
// ──────────────────────────────────────────────────────────────────────────

// importAllowed reports whether the requester may run a CSV import: the table
// must have create or edit enabled AND authz must permit the matching action.
// Import upserts (create when the ID column is blank, update otherwise), so
// either capability is enough to expose it.
func (c *CRUDTable[T]) importAllowed(r *http.Request) bool {
	az := auth.AuthzOrAllow(c.Authz)
	return (c.CreateEnabled && az.CanCreate(r)) || (c.EditEnabled && az.CanUpdate(r))
}

// makeImportHandler is makeFragmentHandler's analogue for the import routes:
// it gates on importAllowed (create OR update) rather than a single action,
// then writes the handler's fragment.
func (c *CRUDTable[T]) makeImportHandler(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.importAllowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		frag := h(w, r)
		if frag == nil {
			return
		}
		site.Fragment(w, r, frag)
	}
}

// handleExportCSV streams every row matching the current ?q/?sort/?desc as a
// CSV attachment (limit 0 = all rows). Gated on the list permission.
func (c *CRUDTable[T]) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	if !c.authzGate(w, r, "list") {
		return
	}
	q := r.URL.Query()
	results, _, err := c.Data.List(r.Context(), q.Get("q"), q.Get("sort"), q.Get("desc") == "1", 0, 0)
	if failInternal(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// Filename is derived from the model name (e.g. Hero → hero_table.csv),
	// independent of the URL the table was routed under.
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ToLower(c.MetaData.Name)+"_table.csv"))
	// Best-effort: a mid-stream write error can't be reported once the headers
	// (200 + attachment) are out, so there's nothing to do but stop.
	_ = writeCSV(w, c.MetaData, results)
}

// importView builds the import form fragment for this table.
func (c *CRUDTable[T]) importView(errs []string) templ.Component {
	return ImportForm(ImportViewData{
		Title:     "Import " + c.MetaData.DisplayName + " from CSV",
		ActionURL: c.urlBase + "/import",
		Columns:   csvColumnNames(c.MetaData),
		Errors:    errs,
		HXTarget:  modalFormTarget,
	})
}

func (c *CRUDTable[T]) handleImportForm(w http.ResponseWriter, r *http.Request) templ.Component {
	return c.importView(nil) // the client opens the modal on swap
}

// csvSource returns the CSV bytes to import: an uploaded file wins, falling
// back to the pasted textarea. Returns an error message (for the form) when
// neither carries content.
func csvSource(r *http.Request) (io.Reader, error) {
	if f, _, err := r.FormFile("file"); err == nil {
		defer f.Close()
		b, rerr := io.ReadAll(f)
		if rerr != nil {
			return nil, fmt.Errorf("could not read uploaded file: %v", rerr)
		}
		if len(bytes.TrimSpace(b)) > 0 {
			return bytes.NewReader(b), nil
		}
	}
	if text := strings.TrimSpace(r.FormValue("csv")); text != "" {
		return strings.NewReader(text), nil
	}
	return nil, errors.New("no CSV provided — paste text or choose a file")
}

func (c *CRUDTable[T]) handleImportPost(w http.ResponseWriter, r *http.Request) templ.Component {
	src, err := csvSource(r)
	if err != nil {
		return c.importView([]string{err.Error()})
	}
	plan, rowErrs, fatal := parseCSVImport(r.Context(), c.MetaData, c.Data, src)
	if failInternal(w, fatal) {
		return nil
	}
	if len(rowErrs) > 0 {
		// Fail-closed: any bad row rejects the whole file, nothing persisted.
		return c.importView(rowErrs)
	}
	// Phase two — persist. Validation was all-or-nothing; persistence is not
	// transactional across rows (the Accessor has no Tx handle), so a backend
	// error mid-loop can leave earlier rows applied. Report it on the form.
	for i, p := range plan {
		if p.id != 0 {
			_, err = c.Data.Update(r.Context(), p.id, p.row)
		} else {
			_, _, err = c.Data.Create(r.Context(), p.row)
		}
		if err != nil {
			return c.importView([]string{
				fmt.Sprintf("applied %d row(s), then failed persisting row %d: %v", i, i+1, err),
			})
		}
	}
	if htmx.IsRequest(r) {
		return c.afterMutation(w, r)
	}
	http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
	return nil
}
