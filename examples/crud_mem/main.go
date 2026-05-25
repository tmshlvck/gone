// Example: full CRUD over a multi-row in-memory map. Demonstrates
// crud.CRUDTable[T] + crud.DeriveMapCRUDTable[T] + crud.Route — list,
// create, edit, delete on the /heroes URL prefix, plus ?q= search and
// ?sort= column ordering driven by MetaField.Sortable / Searchable.
package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/tmshlvck/gone/crud"
)

type Hero struct {
	ID      uint
	Name    string
	Realm   string
	Power   int
	Active  bool
}

func main() {
	store := map[uint]Hero{
		1: {ID: 1, Name: "Aragorn", Realm: "Gondor", Power: 90, Active: true},
		2: {ID: 2, Name: "Legolas", Realm: "Mirkwood", Power: 85, Active: true},
		3: {ID: 3, Name: "Gandalf", Realm: "Middle-earth", Power: 99, Active: true},
		4: {ID: 4, Name: "Boromir", Realm: "Gondor", Power: 70, Active: false},
	}
	var mu sync.RWMutex

	mm, err := crud.DeriveMetaModel[Hero]()
	if err != nil {
		log.Fatalf("derive: %v", err)
	}
	mm.DisplayName = "Heroes"

	// ID column: shown but not editable.
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "ID":
			mm.Fields[i].ReadOnly = true
			mm.Fields[i].Sortable = true
		}
	}

	table := crud.DeriveMapCRUDTable[Hero](store, &mu, mm)
	table.URLBase = "/heroes" // override default ("/hero")

	mux := http.NewServeMux()
	if err := table.Route(mux, pageShell); err != nil {
		log.Fatalf("route: %v", err)
	}

	// Friendly index redirect.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, table.URLBase, http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("crud_mem listening on %s — open %s", addr, table.URLBase)
	log.Fatal(http.ListenAndServe(addr, mux))
}
