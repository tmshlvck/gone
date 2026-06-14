package crud

import "github.com/a-h/templ"

// RenderDisplay returns the bare key/value table for an instance — no chrome,
// no Edit button, no header. The caller's pageShell or modal wrapper supplies
// surrounding markup. The CRUDTable's /{id}/display endpoint serves exactly
// this; applications that own their own routing call it directly.
func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component {
	return DisplayView(DisplayViewData{
		Fields: mm.Fields,
		Cells:  mm.DisplayValues(instance),
	})
}
