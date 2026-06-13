// Example: full CRUD over a multi-row in-memory map. Demonstrates
// crud.CRUDTable[T] + crud.DeriveMapCRUDTable[T] + crud.Route — list,
// create, edit, delete on /heroes, plus ?q= search, ?sort= column sort,
// ?page= pagination, and HTMX-driven modal forms.
package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/crud"
)

type Hero struct {
	ID     uint
	Name   string
	Realm  string
	Power  int
	Active bool
}

// seedHeroes returns ~36 rows so pagination has something to chew on.
func seedHeroes() map[uint]Hero {
	rows := []Hero{
		{Name: "Aragorn", Realm: "Gondor", Power: 90, Active: true},
		{Name: "Legolas", Realm: "Mirkwood", Power: 85, Active: true},
		{Name: "Gandalf", Realm: "Middle-earth", Power: 99, Active: true},
		{Name: "Boromir", Realm: "Gondor", Power: 70, Active: false},
		{Name: "Frodo", Realm: "Shire", Power: 40, Active: true},
		{Name: "Samwise", Realm: "Shire", Power: 55, Active: true},
		{Name: "Merry", Realm: "Shire", Power: 35, Active: true},
		{Name: "Pippin", Realm: "Shire", Power: 30, Active: true},
		{Name: "Gimli", Realm: "Erebor", Power: 75, Active: true},
		{Name: "Galadriel", Realm: "Lothlórien", Power: 95, Active: true},
		{Name: "Elrond", Realm: "Rivendell", Power: 92, Active: true},
		{Name: "Arwen", Realm: "Rivendell", Power: 68, Active: true},
		{Name: "Éowyn", Realm: "Rohan", Power: 80, Active: true},
		{Name: "Éomer", Realm: "Rohan", Power: 78, Active: true},
		{Name: "Théoden", Realm: "Rohan", Power: 65, Active: false},
		{Name: "Faramir", Realm: "Gondor", Power: 72, Active: true},
		{Name: "Denethor", Realm: "Gondor", Power: 60, Active: false},
		{Name: "Saruman", Realm: "Isengard", Power: 94, Active: false},
		{Name: "Radagast", Realm: "Mirkwood", Power: 65, Active: true},
		{Name: "Treebeard", Realm: "Fangorn", Power: 88, Active: true},
		{Name: "Thranduil", Realm: "Mirkwood", Power: 82, Active: true},
		{Name: "Bilbo", Realm: "Shire", Power: 38, Active: false},
		{Name: "Glorfindel", Realm: "Rivendell", Power: 89, Active: true},
		{Name: "Celeborn", Realm: "Lothlórien", Power: 80, Active: true},
		{Name: "Haldir", Realm: "Lothlórien", Power: 70, Active: true},
		{Name: "Beregond", Realm: "Gondor", Power: 50, Active: true},
		{Name: "Hama", Realm: "Rohan", Power: 45, Active: false},
		{Name: "Gríma", Realm: "Rohan", Power: 30, Active: false},
		{Name: "Bard", Realm: "Dale", Power: 76, Active: true},
		{Name: "Thorin", Realm: "Erebor", Power: 84, Active: false},
		{Name: "Balin", Realm: "Erebor", Power: 62, Active: false},
		{Name: "Dwalin", Realm: "Erebor", Power: 71, Active: true},
		{Name: "Kíli", Realm: "Erebor", Power: 58, Active: false},
		{Name: "Fíli", Realm: "Erebor", Power: 60, Active: false},
		{Name: "Beorn", Realm: "Anduin Vales", Power: 86, Active: true},
		{Name: "Tom Bombadil", Realm: "Old Forest", Power: 97, Active: true},
	}
	store := make(map[uint]Hero, len(rows))
	for i, h := range rows {
		id := uint(i + 1)
		h.ID = id
		store[id] = h
	}
	return store
}

func main() {
	store := seedHeroes()
	var mu sync.RWMutex

	// The whole table — identity, paging, and per-field tweaks — is
	// described once, declaratively. NewMapTable reflects Hero, merges these
	// overrides over the reflected defaults, and panics at startup on a
	// typo'd field name (regexp.MustCompile idiom).
	table := crud.NewMapTable(store, &mu, crud.Table[Hero]{
		Slug:     "heroes",
		Title:    "Heroes",
		PageSize: 10,
		Fields: crud.Fields{
			"ID":    {ReadOnly: true},
			"Name":  {Help: "Display name, 2–30 characters.", Validate: crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(30))},
			"Realm": {Help: "Origin (e.g. Gondor, Mirkwood).", Validate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
			"Power": {Help: "Power level, 0–100.", Validate: crud.IntRange(0, 100)},
		},
	})

	mux := chi.NewRouter()
	// The library registers only the table's fragment endpoints; the app
	// owns the page route, embedding table.Render(r) in its own chrome.
	table.RegisterRoutes(mux, "", table.Slug)
	mux.Get("/"+table.Slug, func(w http.ResponseWriter, r *http.Request) {
		content, err := table.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pageShell(w, r, "Heroes", content)
	})
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, table.URLBase(), http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("crud_mem listening on %s — open %s", addr, table.URLBase())
	log.Fatal(http.ListenAndServe(addr, mux))
}

// pageShell wraps the library's component output in the app's chrome.
// It's a PageShellFunc — gets (w, r, title, content). Free to redirect
// or write headers directly; here we just render the templ.
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageLayout(title, content).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}
