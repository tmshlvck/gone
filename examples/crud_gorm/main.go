// Example: three GORM-backed CRUDTables — Hero, Weapon, Skill — wired
// together with a 1:N (Hero has many Weapons) and an N:M (Hero ↔ Skill)
// relation. Each CRUD lives at its own URL (/heroes, /weapons, /skills);
// HTMX uses two stacked modal dialogs at the page-shell level for
// create/edit (L1) and for nested "+ create new" from relation pickers
// (L2). The library exports those dialogs via crud.PageModals() — the
// app embeds them once and every CRUDTable on every page reuses them.
//
// Seeds ~50 heroes / 60 weapons / 12 skills so pagination kicks in at
// the default 10-rows-per-page.
//
// The wiring is three functions: migrate, seed, buildTables. main()
// chains them together and starts the server. All per-MetaField
// customization (FormHelp, FieldValidate, ReadOnly) lives in buildTables
// and goes through MetaModel.FindField; cross-table relation links are
// wired in main() via crud.WireRelations after the tables are routed.
package main

import (
	"log"
	"math/rand"
	"net/http"

	"github.com/a-h/templ"
	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
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
	heroNames   = []string{"Aragorn", "Legolas", "Gandalf", "Boromir", "Frodo", "Samwise", "Merry", "Pippin", "Gimli", "Galadriel", "Elrond", "Arwen", "Éowyn", "Éomer", "Théoden", "Faramir", "Denethor", "Saruman", "Radagast", "Treebeard", "Thranduil", "Bilbo", "Glorfindel", "Celeborn", "Haldir", "Beregond", "Hama", "Gríma", "Bard", "Thorin", "Balin", "Dwalin", "Kíli", "Fíli", "Beorn", "Tom Bombadil", "Lúthien", "Beren", "Eärendil", "Maedhros", "Finrod", "Fëanor", "Túrin", "Húrin", "Idril", "Tuor", "Olwë", "Círdan", "Maglor", "Gil-galad"}
	weaponKinds = []string{"sword", "axe", "bow", "staff", "spear", "dagger", "warhammer", "mace"}
	weaponNames = []string{"Andúril", "Glamdring", "Sting", "Orcrist", "Hadhafang", "Aeglos", "Anguirel", "Aranrúth", "Belthronding", "Bregor", "Dagmor", "Dailir", "Dramborleg", "Galadhrim Bow", "Grond", "Gurthang", "Herugrim", "Ringil", "Narsil", "Belegthronding", "Cirith Erebor", "Daerwen", "Eärendil's Blade", "Foe-hammer", "Gargun's Edge", "Helvengr", "Iron Strike", "Kingfoil Blade", "Leaf Cutter", "Mithril Edge", "Mountain Cleaver", "Nightbringer", "Oakheart", "Pathfinder", "Quickwind", "Ravenfeather", "Stormbreaker", "Tindómiel", "Undómiel's Bow", "Valar's Wrath", "Westcrown", "Xilbalba", "Yavanna's Branch", "Zircon Edge", "Aerin's Bow", "Brytta", "Calenardhon", "Doomforge", "Elendil's Spear", "Felagund's Mace", "Goldwine", "Hithril", "Isen's Edge", "Jorel's Pike", "Kingsbane", "Leithian", "Mournblade", "Nightshade", "Onyx Spear", "Pyre"}
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
		{"Climbing", "Athletics", 4},
		{"Songcraft", "Magic", 6},
	}
)

// migrate creates / updates the schema for the three models.
func migrate(db *gorm.DB) error {
	return db.AutoMigrate(&Hero{}, &Weapon{}, &Skill{})
}

// seed inserts deterministic rows so pagination and relation pickers
// have something to chew on. Skills are created first because Heroes
// reference them by ID for their m2m wiring.
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
		heroes[i] = Hero{
			Name:   n,
			Realm:  realmList[rng.Intn(len(realmList))],
			Power:  20 + rng.Intn(80),
			Active: rng.Intn(4) != 0,
			Skills: pickN(rng, skills, 1+rng.Intn(3)),
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

// buildTables derives the three CRUDTables and configures every MetaField
// in one place (no per-model helpers — every customization is reachable
// from this function). Cross-table relation links are established later, in
// main(), by crud.WireRelations once the tables have been routed (a relation
// picker loads its options from the related table's URL).
//
// MustFindField panics on a typo / renamed field, so the program
// fails fast at startup rather than at form-render time.
func buildTables(db *gorm.DB) (
	heroTable crud.CRUDTable[Hero],
	weaponTable crud.CRUDTable[Weapon],
	skillTable crud.CRUDTable[Skill],
) {
	heroMM, err := crud.DeriveMetaModel[Hero]()
	if err != nil {
		log.Fatal(err)
	}
	weaponMM, err := crud.DeriveMetaModel[Weapon]()
	if err != nil {
		log.Fatal(err)
	}
	skillMM, err := crud.DeriveMetaModel[Skill]()
	if err != nil {
		log.Fatal(err)
	}
	heroMM.DisplayName = "Heroes"
	weaponMM.DisplayName = "Weapons"
	skillMM.DisplayName = "Skills"

	// Hero MetaFields.
	heroMM.MustFindField("ID").ReadOnly = true
	{
		f := heroMM.MustFindField("Name")
		f.FormHelp = "Display name, 2–40 characters."
		f.FieldValidate = crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))
	}
	{
		f := heroMM.MustFindField("Realm")
		f.FormHelp = "Origin (e.g. Gondor, Mirkwood)."
		f.FieldValidate = crud.MaxLen(40)
	}
	{
		f := heroMM.MustFindField("Power")
		f.FormHelp = "Power level, 0–100."
		f.FieldValidate = crud.IntRange(0, 100)
	}
	{
		f := heroMM.MustFindField("Weapons")
		f.DisplayName = "Weapons (read-only)"
		f.FormHelp = "Edit weapons individually via /weapons."
	}
	heroMM.MustFindField("Skills").FormHelp = "Hold Ctrl/Cmd to pick multiple."

	// Weapon MetaFields.
	weaponMM.MustFindField("ID").ReadOnly = true
	weaponMM.MustFindField("Name").FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(50))
	weaponMM.MustFindField("Kind").FormHelp = "Weapon type (sword, axe, …)."
	{
		f := weaponMM.MustFindField("Damage")
		f.FormHelp = "Damage rating, 1–100."
		f.FieldValidate = crud.IntRange(0, 100)
	}
	weaponMM.MustFindField("Owner").FormHelp = "Wielder. Pick one or use + to create a new hero."

	// Skill MetaFields.
	skillMM.MustFindField("ID").ReadOnly = true
	skillMM.MustFindField("Name").FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(40))
	skillMM.MustFindField("School").FormHelp = "Combat / Magic / Roguery / ..."
	{
		f := skillMM.MustFindField("Level")
		f.FormHelp = "Mastery, 1–10."
		f.FieldValidate = crud.IntRange(0, 10)
	}
	// Skill ↔ Hero is m2m editable in principle, but the app flow
	// assigns skills via the Hero form — keep Heroes ReadOnly so it
	// shows in the dump but is skipped in the Skill form.
	skillMM.MustFindField("Heroes").ReadOnly = true

	heroTable = crud.DeriveGormCRUDTable[Hero](heroMM, nil, db)
	weaponTable = crud.DeriveGormCRUDTable[Weapon](weaponMM, nil, db)
	skillTable = crud.DeriveGormCRUDTable[Skill](skillMM, nil, db)
	heroTable.Slug = "heroes"
	weaponTable.Slug = "weapons"
	skillTable.Slug = "skills"
	heroTable.PageSize = 10
	weaponTable.PageSize = 10
	skillTable.PageSize = 8

	// Cross-table relation links are wired by crud.WireRelations in main(),
	// after the tables are routed (it matches each relation field's type
	// against the routed tables' URLs — see below).
	return
}

// ─── Wiring ────────────────────────────────────────────────────────────

func main() {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("gorm open: %v", err)
	}
	if err := migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}
	heroTable, weaponTable, skillTable := buildTables(db)

	mux := chi.NewRouter()
	// The library registers each table's fragment endpoints; the app owns
	// the page route (GET /{slug}) that embeds table.Render(r) in chrome.
	mountPage(mux, &heroTable, "Heroes")
	mountPage(mux, &weaponTable, "Weapons")
	mountPage(mux, &skillTable, "Skills")
	// Link the relation pickers now that every table has its URL: the
	// Owner select on /weapons loads options from /heroes, etc.
	crud.WireRelations(&heroTable, &weaponTable, &skillTable)
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, heroTable.URLBase(), http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("crud_gorm listening on %s — open /heroes, /weapons, /skills", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// pageComponent is the slice of a CRUDTable the app needs to mount a page.
type pageComponent interface {
	RegisterRoutes(r chi.Router, mountBase, slug string)
	Render(r *http.Request) (templ.Component, error)
	URLSlug() string
	URLBase() string
}

// mountPage registers a table's fragment endpoints plus the app-owned page
// route that wraps its Render output in pageShell.
func mountPage(mux chi.Router, t pageComponent, title string) {
	t.RegisterRoutes(mux, "", t.URLSlug())
	mux.Get("/"+t.URLSlug(), func(w http.ResponseWriter, r *http.Request) {
		content, err := t.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pageShell(w, r, title, content)
	})
}

// pageShell wraps the library's component output in the app's chrome
// (templ pageLayout). Implements crud.PageShellFunc — gets (w, r,
// title, content). Free to redirect or write headers; here it just
// renders the templ.
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageLayout(title, content).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}
