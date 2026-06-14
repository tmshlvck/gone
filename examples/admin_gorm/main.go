// Example: Admin over three GORM-backed CRUDTables — Hero, Weapon, Skill —
// with near-zero per-field config. Admin links the cross-table relations
// automatically at RegisterRoutes time and shows one table at a time behind a
// sidebar (plain links, full page loads). Also keeps the dark/light toggle.
package main

import (
	"log"
	"math/rand"
	"net/http"

	"github.com/a-h/templ"
	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmshlvck/gone/crud"
	"gorm.io/gorm"
)

// ─── Schema ────────────────────────────────────────────────────────────

type Hero struct {
	ID      uint
	Name    string
	Realm   string
	Power   int
	Active  bool
	Weapons []Weapon `gorm:"foreignKey:OwnerID;constraint:OnDelete:SET NULL"`
	Skills  []Skill  `gorm:"many2many:hero_skills"`
}

type Weapon struct {
	ID      uint
	Name    string
	Kind    string
	Damage  int
	OwnerID uint
	Owner   Hero `gorm:"foreignKey:OwnerID"`
}

type Skill struct {
	ID     uint
	Name   string
	School string
	Level  int
	Heroes []Hero `gorm:"many2many:hero_skills"`
}

// ─── Seed catalogue ────────────────────────────────────────────────────

var (
	realmList   = []string{"Gondor", "Mirkwood", "Shire", "Rohan", "Erebor", "Rivendell", "Lothlórien", "Fangorn", "Dale", "Isengard"}
	heroNames   = []string{"Aragorn", "Legolas", "Gandalf", "Boromir", "Frodo", "Samwise", "Merry", "Pippin", "Gimli", "Galadriel", "Elrond", "Arwen", "Éowyn", "Éomer", "Théoden", "Faramir", "Denethor", "Saruman", "Radagast", "Treebeard", "Thranduil", "Bilbo", "Glorfindel", "Celeborn", "Haldir", "Beregond", "Hama", "Gríma", "Bard", "Thorin"}
	weaponKinds = []string{"sword", "axe", "bow", "staff", "spear", "dagger", "warhammer", "mace"}
	weaponNames = []string{"Andúril", "Glamdring", "Sting", "Orcrist", "Hadhafang", "Aeglos", "Anguirel", "Aranrúth", "Belthronding", "Bregor", "Dagmor", "Dailir", "Dramborleg", "Galadhrim Bow", "Grond", "Gurthang", "Herugrim", "Ringil", "Narsil", "Belegthronding"}
	skillsList  = []struct {
		Name, School string
		Level        int
	}{
		{"Swordsmanship", "Combat", 8},
		{"Archery", "Combat", 7},
		{"Stealth", "Roguery", 6},
		{"Pyromancy", "Magic", 9},
		{"Healing", "Magic", 5},
		{"Tracking", "Survival", 4},
		{"Riding", "Athletics", 5},
		{"Diplomacy", "Social", 6},
		{"Lore", "Knowledge", 7},
		{"Smithing", "Craft", 5},
	}
)

func migrate(db *gorm.DB) error {
	return db.AutoMigrate(&Hero{}, &Weapon{}, &Skill{})
}

func seed(db *gorm.DB) error {
	rng := rand.New(rand.NewSource(42))

	skills := make([]Skill, len(skillsList))
	for i, s := range skillsList {
		skills[i] = Skill{Name: s.Name, School: s.School, Level: s.Level}
	}
	if err := db.Create(&skills).Error; err != nil {
		return err
	}
	heroes := make([]Hero, len(heroNames))
	for i, n := range heroNames {
		picked := pickN(rng, skills, 1+rng.Intn(3))
		heroes[i] = Hero{
			Name:   n,
			Realm:  realmList[rng.Intn(len(realmList))],
			Power:  20 + rng.Intn(80),
			Active: rng.Intn(4) != 0,
			Skills: picked,
		}
	}
	if err := db.Create(&heroes).Error; err != nil {
		return err
	}
	weapons := make([]Weapon, 0, len(weaponNames))
	for _, n := range weaponNames {
		w := Weapon{
			Name:   n,
			Kind:   weaponKinds[rng.Intn(len(weaponKinds))],
			Damage: 10 + rng.Intn(90),
		}
		if rng.Intn(10) != 0 {
			w.OwnerID = heroes[rng.Intn(len(heroes))].ID
		}
		weapons = append(weapons, w)
	}
	return db.Create(&weapons).Error
}

func pickN[T any](rng *rand.Rand, src []T, n int) []T {
	if n > len(src) {
		n = len(src)
	}
	idxs := rng.Perm(len(src))[:n]
	out := make([]T, n)
	for i, ix := range idxs {
		out[i] = src[ix]
	}
	return out
}

// ─── Wiring ────────────────────────────────────────────────────────────

func main() {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("gorm: %v", err)
	}
	if err := migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	// Default tables (slugs heros / weapons / skills). Hero overrides
	// ShortLabel so it reads "Name — Realm" wherever it appears as a relation
	// (the Owner picker + cells on /weapons); drop it for the Name-only default.
	heroMM := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{})
	heroTable := crud.NewTable(heroMM, crud.GORMAccessor(heroMM, db), 0, nil)
	heroTable.ShortLabel = func(h Hero) string { return h.Name + " — " + h.Realm }

	weaponMM := crud.DeriveMetaModel[Weapon](crud.MetaModel[Weapon]{})
	weaponTable := crud.NewTable(weaponMM, crud.GORMAccessor(weaponMM, db), 0, nil)

	skillMM := crud.DeriveMetaModel[Skill](crud.MetaModel[Skill]{})
	skillTable := crud.NewTable(skillMM, crud.GORMAccessor(skillMM, db), 0, nil)

	// Admin auto-wires the relations (by matching related type name to the
	// managed tables) when it registers their routes.
	admin := crud.DeriveAdmin([]crud.CRUDTableInterface{&heroTable, &weaponTable, &skillTable}, nil)

	// Custom sidebar link → /testlink (a fragment under HTMX, full page direct).
	admin.SidebarBottom = []crud.SidebarLink{
		{Separator: true},
		{DisplayName: "Hello", URL: "/testlink"},
	}

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	if err := admin.RegisterRoutes(mux, "", "/admin", pageShell); err != nil {
		log.Fatalf("admin route: %v", err)
	}
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	mux.Get("/testlink", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := helloFragment().Render(r.Context(), w); err != nil {
				log.Printf("render: %v", err)
			}
			return
		}
		pageShell(w, r, "Hello", helloFragment())
	})

	log.Printf("admin_gorm listening on :8080 — open /admin")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// pageShell renders the library's component inside the app's HTML chrome.
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageLayout(title, content).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}
