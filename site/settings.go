package site

// PaginationSettings is the app-global default for CRUDTable paging.
type PaginationSettings interface {
	// PaginationSizeDefault is the default rows-per-page for tables that
	// don't set their own size. 0 means no pagination — show every row.
	PaginationSizeDefault() uint16
}

// Settings is the app-global presentation + behavior config gone components
// consult: time formatting (TimeFormatter) plus pagination defaults
// (PaginationSettings). It's an object the app owns and passes to components
// (e.g. crud.MetaModel.Settings), not carried on the request context, so the
// same config is reusable outside HTTP.
//
// Override by embedding DefaultSettings and shadowing one method; because
// consumers hold the interface, dynamic dispatch picks up the override and the
// untouched concerns keep their defaults. Embedding also keeps you compiling
// as the interface grows.
//
//	type appSettings struct{ site.DefaultSettings }
//	func (appSettings) PaginationSizeDefault() uint16 { return 50 } // time format stays default
type Settings interface {
	TimeFormatter
	PaginationSettings
}

// DefaultSettings is the canonical Settings: DefaultTimeFormatter formatting
// and a 20-row default page size. Embed it to override one concern.
type DefaultSettings struct {
	DefaultTimeFormatter
}

func (DefaultSettings) PaginationSizeDefault() uint16 { return 20 }

// fixedPageSize is the PaginationSettings returned by PageSize.
type fixedPageSize uint16

func (f fixedPageSize) PaginationSizeDefault() uint16 { return uint16(f) }

// PageSize returns a PaginationSettings with a fixed default of n rows per
// page — for a table that wants its own size rather than the app-wide
// DefaultSettings. PageSize(0) means no pagination (show every row).
//
//	crud.NewTable(mm, data, site.PageSize(10), authz) // 10 per page
//	crud.NewTable(mm, data, site.PageSize(0), authz)  // all rows
func PageSize(n uint16) PaginationSettings { return fixedPageSize(n) }
