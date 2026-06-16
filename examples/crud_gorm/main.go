// Example: three GORM-backed CRUDTables — Hero, Weapon, Skill — with a 1:N
// (Hero has many Weapons) and an N:M (Hero ↔ Skill) relation, each at its own
// URL (/heroes, /weapons, /skills). crud.WireRelations links the relation
// pickers after the tables are routed.
package main

import (
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmshlvck/gone/crud"
	"github.com/tmshlvck/gone/site"
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
	Forged  time.Time // when the weapon was forged — showcases time handling
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

	// A fixed reference instant so the seed stays deterministic; each
	// weapon is forged some random span before it.
	forgeBase := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	weapons := make([]Weapon, 0, len(weaponNames))
	for _, n := range weaponNames {
		w := Weapon{
			Name:   n,
			Kind:   weaponKinds[rng.Intn(len(weaponKinds))],
			Damage: 10 + rng.Intn(90),
			Forged: forgeBase.Add(-time.Duration(rng.Intn(5000)) * time.Hour),
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

// buildTables builds the three tables via the three-step construction:
// DeriveMetaModel (reflect + per-field overrides) → GORMAccessor → NewTable.
// Path is supplied later, at RegisterRoutes.
func buildTables(db *gorm.DB) (
	heroTable crud.CRUDTable[Hero],
	weaponTable crud.CRUDTable[Weapon],
	skillTable crud.CRUDTable[Skill],
) {
	heroMM := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
		DisplayName: "Heroes",
		Fields: []crud.MetaField{
			{Name: "ID", ReadOnly: true},
			{Name: "Name", FormHelp: "Display name, 2–40 characters.", FieldValidate: crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))},
			{Name: "Realm", FormHelp: "Origin (e.g. Gondor, Mirkwood).", FieldValidate: crud.MaxLen(40)},
			{Name: "Power", FormHelp: "Power level, 0–100.", FieldValidate: crud.IntRange(0, 100)},
			{Name: "Weapons", DisplayName: "Weapons (read-only)", FormHelp: "Edit weapons individually via /weapons."},
			{Name: "Skills", FormHelp: "Hold Ctrl/Cmd to pick multiple."},
		},
	})
	heroTable = crud.NewTable(heroMM, crud.GORMAccessor(heroMM, db), 10, nil)

	weaponMM := crud.DeriveMetaModel[Weapon](crud.MetaModel[Weapon]{
		DisplayName: "Weapons",
		Fields: []crud.MetaField{
			{Name: "ID", ReadOnly: true},
			{Name: "Name", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(50))},
			{Name: "Kind", FormHelp: "Weapon type (sword, axe, …)."},
			{Name: "Damage", FormHelp: "Damage rating, 1–100.", FieldValidate: crud.IntRange(0, 100)},
			{Name: "Owner", FormHelp: "Wielder. Pick one or use + to create a new hero."},
		},
	})
	weaponTable = crud.NewTable(weaponMM, crud.GORMAccessor(weaponMM, db), 10, nil)

	skillMM := crud.DeriveMetaModel[Skill](crud.MetaModel[Skill]{
		DisplayName: "Skills",
		Fields: []crud.MetaField{
			{Name: "ID", ReadOnly: true},
			{Name: "Name", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
			{Name: "School", FormHelp: "Combat / Magic / Roguery / ..."},
			{Name: "Level", FormHelp: "Mastery, 1–10.", FieldValidate: crud.IntRange(0, 10)},
			// Skill ↔ Hero is m2m editable in principle, but the app flow
			// assigns skills via the Hero form — ReadOnly keeps Heroes in the
			// dump but skips it in the Skill form.
			{Name: "Heroes", ReadOnly: true},
		},
	})
	skillTable = crud.NewTable(skillMM, crud.GORMAccessor(skillMM, db), 8, nil)
	return
}

// ─── Wiring ────────────────────────────────────────────────────────────

func main() {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("gorm open: %v", err)
	}
	// Store every time.Time in UTC, regardless of backend — so the Forged
	// column sorts and filters by instant, not by wall-clock text. Call
	// once, before any writes.
	if err := site.ForceUTC(db); err != nil {
		log.Fatalf("ForceUTC: %v", err)
	}
	if err := migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}
	heroTable, weaponTable, skillTable := buildTables(db)

	// Per-session display timezone: a navbar picker (UTC / browser-local /
	// any of CommonZones) stores the choice in a cookie; the middleware
	// resolves it onto each request's context so the Weapon Forged column and
	// its edit form render and parse in the chosen zone. Storage stays UTC.
	tz := &site.TimezonePicker{Mode: site.TZModeFull, Zones: site.CommonZones}

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	mux.Use(site.TimezoneMiddleware(tz.Resolve))
	tz.RegisterRoutes(mux)
	mountPage(mux, &heroTable, "/heroes", "Heroes", tz)
	mountPage(mux, &weaponTable, "/weapons", "Weapons", tz)
	mountPage(mux, &skillTable, "/skills", "Skills", tz)
	// Link the relation pickers now every table has its URL (Owner select on
	// /weapons loads options from /heroes, etc).
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
	RegisterRoutes(r chi.Router, routerPrefix, componentPath string)
	Render(r *http.Request) (templ.Component, error)
	URLBase() string
}

// mountPage registers a table's fragment endpoints at path plus the app-owned
// page route that wraps its Render output in pageShell.
func mountPage(mux chi.Router, t pageComponent, path, title string, tz *site.TimezonePicker) {
	t.RegisterRoutes(mux, "", path)
	mux.Get(path, func(w http.ResponseWriter, r *http.Request) {
		content, err := t.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pageShell(w, r, title, content, tz)
	})
}

// pageShell renders the library's component inside the app's HTML chrome,
// including the timezone picker in the navbar.
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component, tz *site.TimezonePicker) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageLayout(title, tz.Component(r), content).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}
