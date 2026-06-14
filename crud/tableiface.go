package crud

import (
	"context"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
)

// CRUDTableInterface is the non-generic surface Admin and the relation wiring
// need to treat tables of differing T uniformly: identity (display name, URL
// segment, Go model type), the absolute mount URL, page rendering, route
// registration, and relation stamping.
//
// Cross-table option fetching is deliberately NOT here. A relation <select>
// loads its <option> list over HTTP from the *related* table's own /options
// endpoint (see relationSelect) — tables link by URL, not by an in-process
// pointer — so the interface carries no SearchOptions / data accessors.
// *CRUDTable[T] satisfies it.
type CRUDTableInterface interface {
	DisplayName() string
	ModelName() string                                               // Go type name (e.g. "Hero")
	URLSlug() string                                                 // path segment, e.g. "heroes"
	URLBase() string                                                 // absolute URL prefix, e.g. "/admin/heroes"
	Render(r *http.Request) (templ.Component, error)                 // table view + this table's L1 modal
	RegisterRoutes(r chi.Router, routerPrefix, componentPath string) // register the table's fragment endpoints

	// InstanceShortLabel renders one of this model's rows as its short label — the
	// table's ShortLabel override if set, else DefaultShortLabel. instance is
	// a T (or *T).
	InstanceShortLabel(instance any) string

	// StampRelations resolves each relation field's RelatedURLBase (and
	// RelatedShortLabel) from byType (Go type name → the related table).
	// Called after every table has been routed; WireRelations and Admin do
	// this for you.
	StampRelations(byType map[string]CRUDTableInterface)
}

// ──────────────────────────────────────────────────────────────────────────
// CRUDTableInterface implementation for *CRUDTable[T].
// ──────────────────────────────────────────────────────────────────────────

func (c *CRUDTable[T]) DisplayName() string { return c.MetaData.DisplayName }

// ModelName returns the Go type name of the underlying model
// (MetaData.Name) — used by Admin's auto-wire path to match
// MetaField.RelatedTypeName against peer tables.
func (c *CRUDTable[T]) ModelName() string { return c.MetaData.Name }

// URLSlug returns the URL path segment for this table: the Segment override
// when set, else a lowercased plural of the model name. Admin uses it to
// place the child and mark the active sidebar entry.
func (c *CRUDTable[T]) URLSlug() string {
	if c.Segment != "" {
		return strings.Trim(c.Segment, "/")
	}
	return defaultSlug(c.MetaData.Name)
}

// URLBase returns the absolute URL prefix the CRUDTable was routed under
// (e.g. "/admin/heroes"). Set by RegisterRoutes; empty until then.
func (c *CRUDTable[T]) URLBase() string { return c.urlBase }

// StampRelations sets RelatedURLBase on each relation field whose
// RelatedTypeName matches an entry in byType (Go type name → absolute table
// URL). Unmatched relations are left blank — their <select> renders without
// an options endpoint (degraded, but functional). Self-references are
// handled the same way (a table's own type can appear in byType).
func (c *CRUDTable[T]) StampRelations(byType map[string]CRUDTableInterface) {
	for i := range c.MetaData.Fields {
		f := &c.MetaData.Fields[i]
		if f.RelationKind == NotRelation || f.RelatedTypeName == "" {
			continue
		}
		if peer, ok := byType[f.RelatedTypeName]; ok {
			f.RelatedURLBase = peer.URLBase()
			f.RelatedShortLabel = peer.InstanceShortLabel
		}
	}
}

// InstanceShortLabel renders instance (a row of T) as its short label: the
// table's ShortLabel override if set, else DefaultShortLabel. Accepts a T or
// *T.
func (c *CRUDTable[T]) InstanceShortLabel(instance any) string {
	if c.ShortLabel != nil {
		switch v := instance.(type) {
		case T:
			return c.ShortLabel(v)
		case *T:
			if v != nil {
				return c.ShortLabel(*v)
			}
		}
	}
	return DefaultShortLabel(instance)
}

// SearchOptions returns up to relationOptionLimit options matching search.
func (c *CRUDTable[T]) SearchOptions(ctx context.Context, search string) ([]CRUDRelationOption, int64, error) {
	results, total, err := c.Data.List(ctx, search, "", false, 0, relationOptionLimit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]CRUDRelationOption, len(results))
	for i, r := range results {
		out[i] = CRUDRelationOption{
			ID:        r.ID,
			Instance:  r.Row,
			ShortName: c.InstanceShortLabel(r.Row),
		}
	}
	return out, total, nil
}

// relationOptionLimit caps the dropdown to a reasonable number of rows;
// past this you'd want a typeahead instead of a vanilla <select>.
const relationOptionLimit = 500

// WireRelations links relation fields across a set of already-routed tables.
// It builds a Go-type-name → table map, then stamps each table's relation
// fields from it. Call once, AFTER every table's RegisterRoutes (URLBases
// must be set):
//
//	heroTable.RegisterRoutes(r, "", "/heroes")
//	weaponTable.RegisterRoutes(r, "", "/weapons")
//	crud.WireRelations(&heroTable, &weaponTable)
//
// Admin.RegisterRoutes calls this for the tables it manages, so Admin callers
// don't invoke it directly.
func WireRelations(tables ...CRUDTableInterface) {
	byType := make(map[string]CRUDTableInterface, len(tables))
	for _, t := range tables {
		if n := t.ModelName(); n != "" {
			byType[n] = t
		}
	}
	for _, t := range tables {
		t.StampRelations(byType)
	}
}

// shortLabelFor labels a related instance: the related table's stamped label
// fn (RelatedShortLabel, which honors its ShortLabel override) if present,
// else DefaultShortLabel.
func shortLabelFor(mf MetaField, value any) string {
	if mf.RelatedShortLabel != nil {
		return mf.RelatedShortLabel(value)
	}
	return DefaultShortLabel(value)
}
