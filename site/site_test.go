package site

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func comp(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func TestFragmentWritesBareContent(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/heroes/view", nil)
	Fragment(w, r, comp("<tr><td>Aragorn</td></tr>"))

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := w.Body.String(); got != "<tr><td>Aragorn</td></tr>" {
		t.Errorf("body = %q", got)
	}
}

func TestRespondFragmentVsFullPage(t *testing.T) {
	pageShell := Shell(func(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
		_, _ = io.WriteString(w, "<html><title>"+title+"</title>")
		_ = content.Render(r.Context(), w)
		_, _ = io.WriteString(w, "</html>")
	})

	// HTMX request → bare fragment, shell untouched.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/heroes", nil)
	r1.Header.Set("HX-Request", "true")
	Respond(w1, r1, pageShell, "Heroes", comp("FRAG"))
	if got := w1.Body.String(); got != "FRAG" {
		t.Errorf("HTMX body = %q, want bare fragment", got)
	}

	// Plain navigation → full page via shell.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/heroes", nil)
	Respond(w2, r2, pageShell, "Heroes", comp("FRAG"))
	if got := w2.Body.String(); !strings.Contains(got, "<html>") || !strings.Contains(got, "FRAG") {
		t.Errorf("plain body = %q, want full page", got)
	}
}
