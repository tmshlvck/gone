package site

import (
	_ "embed"

	"github.com/a-h/templ"
)

// StylesheetCSS is gone's optional polish layer for DaisyUI v5 (see
// gone.css). It is plain CSS with no build step: serve it as a static
// file, inline it in your page <head>, or use StyleTag() to get a ready
// <style> element. The library never injects it for you — an app opts in,
// and is free to copy, edit, or ignore it. It targets the gone-* hook
// classes the components emit (gone-form, gone-pagination, …) plus the
// DaisyUI component classes, and tames the v5 focus outline that hugs
// control borders.
//
//go:embed gone.css
var StylesheetCSS string

// StyleTag renders StylesheetCSS as an inline <style> element for embedding
// in a page <head>. Place it after the DaisyUI stylesheet so its rules win.
func StyleTag() templ.Component {
	return templ.Raw("<style>" + StylesheetCSS + "</style>")
}
