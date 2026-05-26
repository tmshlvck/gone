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
package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"

	"github.com/glebarez/sqlite"
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

// ─── Seed data ─────────────────────────────────────────────────────────

var (
	realmList  = []string{"Gondor", "Mirkwood", "Shire", "Rohan", "Erebor", "Rivendell", "Lothlórien", "Fangorn", "Dale", "Isengard"}
	heroNames  = []string{"Aragorn", "Legolas", "Gandalf", "Boromir", "Frodo", "Samwise", "Merry", "Pippin", "Gimli", "Galadriel", "Elrond", "Arwen", "Éowyn", "Éomer", "Théoden", "Faramir", "Denethor", "Saruman", "Radagast", "Treebeard", "Thranduil", "Bilbo", "Glorfindel", "Celeborn", "Haldir", "Beregond", "Hama", "Gríma", "Bard", "Thorin", "Balin", "Dwalin", "Kíli", "Fíli", "Beorn", "Tom Bombadil", "Lúthien", "Beren", "Eärendil", "Maedhros", "Finrod", "Fëanor", "Túrin", "Húrin", "Idril", "Tuor", "Olwë", "Círdan", "Maglor", "Gil-galad"}
	weaponKinds = []string{"sword", "axe", "bow", "staff", "spear", "dagger", "warhammer", "mace"}
	weaponNames = []string{"Andúril", "Glamdring", "Sting", "Orcrist", "Hadhafang", "Aeglos", "Anguirel", "Aranrúth", "Belthronding", "Bregor", "Dagmor", "Dailir", "Dramborleg", "Galadhrim Bow", "Grond", "Gurthang", "Herugrim", "Ringil", "Narsil", "Belegthronding", "Cirith Erebor", "Daerwen", "Eärendil's Blade", "Foe-hammer", "Gargun's Edge", "Helvengr", "Iron Strike", "Kingfoil Blade", "Leaf Cutter", "Mithril Edge", "Mountain Cleaver", "Nightbringer", "Oakheart", "Pathfinder", "Quickwind", "Ravenfeather", "Stormbreaker", "Tindómiel", "Undómiel's Bow", "Valar's Wrath", "Westcrown", "Xilbalba", "Yavanna's Branch", "Zircon Edge", "Aerin's Bow", "Brytta", "Calenardhon", "Doomforge", "Elendil's Spear", "Felagund's Mace", "Goldwine", "Hithril", "Isen's Edge", "Jorel's Pike", "Kingsbane", "Leithian", "Mournblade", "Nightshade", "Onyx Spear", "Pyre"}
	skillsList = []struct {
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

func seed(db *gorm.DB) error {
	if err := db.AutoMigrate(&Hero{}, &Weapon{}, &Skill{}); err != nil {
		return err
	}

	rng := rand.New(rand.NewSource(42)) // deterministic seed so reruns produce the same data

	// Skills first — Heroes' m2m wiring references them by ID.
	skills := make([]Skill, len(skillsList))
	for i, s := range skillsList {
		skills[i] = Skill{Name: s.Name, School: s.School, Level: s.Level}
	}
	if err := db.Create(&skills).Error; err != nil {
		return err
	}

	// Heroes with random skill picks.
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

	// Weapons — each owned by a random hero. A few unowned (OwnerID=0).
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
		log.Fatalf("gorm open: %v", err)
	}
	if err := seed(db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	// Derive a MetaModel + CRUDTable for each type. Per-field tweaks
	// (FormHelp, FieldValidate, ReadOnly) and the relation→table wiring
	// come after.
	heroMM, _ := crud.DeriveMetaModel[Hero]()
	weaponMM, _ := crud.DeriveMetaModel[Weapon]()
	skillMM, _ := crud.DeriveMetaModel[Skill]()

	heroMM.DisplayName = "Heroes"
	weaponMM.DisplayName = "Weapons"
	skillMM.DisplayName = "Skills"

	annotateHero(&heroMM)
	annotateWeapon(&weaponMM)
	annotateSkill(&skillMM)

	heroTable := crud.DeriveGormCRUDTable[Hero](db, heroMM)
	weaponTable := crud.DeriveGormCRUDTable[Weapon](db, weaponMM)
	skillTable := crud.DeriveGormCRUDTable[Skill](db, skillMM)
	heroTable.URLBase = "/heroes"
	weaponTable.URLBase = "/weapons"
	skillTable.URLBase = "/skills"
	heroTable.PageSize = 10
	weaponTable.PageSize = 10
	skillTable.PageSize = 8

	// Wire relations — each MetaField.RelatedCRUD points at the
	// matching CRUDTable so DefaultGenFormElement can render the
	// <select> with the right options + "+ new" button.
	wireRelation(&heroTable.MetaData, "Weapons", &weaponTable)
	wireRelation(&heroTable.MetaData, "Skills", &skillTable)
	wireRelation(&weaponTable.MetaData, "Owner", &heroTable)
	wireRelation(&skillTable.MetaData, "Heroes", &heroTable)

	mux := http.NewServeMux()
	must(heroTable.Route(mux))
	must(weaponTable.Route(mux))
	must(skillTable.Route(mux))

	// Every page-shell embeds crud.PageModals() so the two shared
	// dialogs are in the DOM regardless of which table is on screen.
	registerPage(mux, &heroTable, "Heroes")
	registerPage(mux, &weaponTable, "Weapons")
	registerPage(mux, &skillTable, "Skills")

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/heroes", http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("crud_gorm listening on %s — open /heroes, /weapons, /skills", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func annotateHero(mm *crud.MetaModel[Hero]) {
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "ID":
			mm.Fields[i].ReadOnly = true
		case "Name":
			mm.Fields[i].FormHelp = "Display name, 2–40 characters."
			mm.Fields[i].FieldValidate = crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))
		case "Realm":
			mm.Fields[i].FormHelp = "Origin (e.g. Gondor, Mirkwood)."
			mm.Fields[i].FieldValidate = crud.MaxLen(40)
		case "Power":
			mm.Fields[i].FormHelp = "Power level, 0–100."
			mm.Fields[i].FieldValidate = crud.IntRange(0, 100)
		case "Active":
			mm.Fields[i].DisplayName = "Active"
		case "Weapons":
			mm.Fields[i].DisplayName = "Weapons (read-only)"
			mm.Fields[i].FormHelp = "Edit weapons individually via /weapons."
		case "Skills":
			mm.Fields[i].FormHelp = "Hold Ctrl/Cmd to pick multiple."
		}
	}
}

func annotateWeapon(mm *crud.MetaModel[Weapon]) {
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "ID":
			mm.Fields[i].ReadOnly = true
		case "Name":
			mm.Fields[i].FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(50))
		case "Kind":
			mm.Fields[i].FormHelp = "Weapon type (sword, axe, …)."
		case "Damage":
			mm.Fields[i].FormHelp = "Damage rating, 1–100."
			mm.Fields[i].FieldValidate = crud.IntRange(0, 100)
		case "Owner":
			mm.Fields[i].FormHelp = "Wielder. Pick one or use + to create a new hero."
		}
	}
}

func annotateSkill(mm *crud.MetaModel[Skill]) {
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "ID":
			mm.Fields[i].ReadOnly = true
		case "Name":
			mm.Fields[i].FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(40))
		case "School":
			mm.Fields[i].FormHelp = "Combat / Magic / Roguery / ..."
		case "Level":
			mm.Fields[i].FormHelp = "Mastery, 1–10."
			mm.Fields[i].FieldValidate = crud.IntRange(0, 10)
		case "Heroes":
			// Skill ↔ Hero is m2m editable in principle, but the app
			// flow assigns skills via the Hero form. Mark Heroes
			// ReadOnly so it shows in the dump but is skipped in the
			// Skill form.
			mm.Fields[i].ReadOnly = true
		}
	}
}

// wireRelation sets MetaField.RelatedCRUD on the named field. The
// crud.CRUDTableInterface is satisfied by *crud.CRUDTable[T] regardless
// of the concrete T.
func wireRelation[T any](mm *crud.MetaModel[T], fieldName string, related crud.CRUDTableInterface) {
	for i := range mm.Fields {
		if mm.Fields[i].Name == fieldName {
			mm.Fields[i].RelatedCRUD = related
			return
		}
	}
	log.Printf("wireRelation: no field %q on %s", fieldName, mm.Name)
}

func registerPage[T any](mux *http.ServeMux, tbl *crud.CRUDTable[T], title string) {
	mux.HandleFunc("GET "+tbl.URLBase, func(w http.ResponseWriter, r *http.Request) {
		comp, err := tbl.RenderComponent(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageShell(title, comp).Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	})
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// guard against unused
var _ = fmt.Sprintf
