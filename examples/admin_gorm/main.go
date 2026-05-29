// Example: Admin over three GORM-backed CRUDTables — Hero, Weapon,
// Skill — with zero per-field tweaking. Every MetaModel and CRUDTable
// uses the library defaults, and DeriveAdminAutoWire fills in the
// cross-table RelatedCRUD pointers by matching field types against
// peer ModelName().
//
// The sidebar HTMX-swaps each table into the working pane on click;
// the URL updates via hx-push-url so bookmarking the active model
// works. The page-shell wraps admin.Render and embeds the shared L2
// modal via crud.PageModals.
package main

import (
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

	// Every model goes through plain Derive*: no FormHelp, no
	// FieldValidate, no manual RelatedCRUD wiring. Just defaults.
	heroMM, _ := crud.DeriveMetaModel[Hero]()
	weaponMM, _ := crud.DeriveMetaModel[Weapon]()
	skillMM, _ := crud.DeriveMetaModel[Skill]()

	heroTable := crud.DeriveGormCRUDTable[Hero](heroMM, nil, db)
	weaponTable := crud.DeriveGormCRUDTable[Weapon](weaponMM, nil, db)
	skillTable := crud.DeriveGormCRUDTable[Skill](skillMM, nil, db)

	mux := http.NewServeMux()

	// DeriveAdminAutoWire walks every table's relation fields and
	// matches the related type name (Hero / Weapon / Skill) against
	// each peer's ModelName(), setting RelatedCRUD in place. Without
	// this step the relation pickers would render with no options.
	tables := []crud.CRUDTableInterface{&heroTable, &weaponTable, &skillTable}
	admin := crud.DeriveAdminAutoWire(tables, nil)

	// admin.Route owns the children — it auto-routes each table at
	// /admin/{slug} and registers /admin → 303 to /admin/{first.Slug}.
	// Default slugs are "heros" / "weapons" / "skills" (lowercase+"s");
	// the irregular Hero→heroes plural is left as-is for this demo.
	if err := admin.Route(mux, "/admin"); err != nil {
		log.Fatalf("admin route: %v", err)
	}

	// Per-slug page route — wraps admin.Render in the page-shell. One
	// pattern catches /admin/heros, /admin/weapons, /admin/skills.
	// Each CRUDTable's own sub-routes (/admin/heros/view, /create, …)
	// are more specific and win the pattern match.
	adminPage := func(w http.ResponseWriter, r *http.Request) {
		comp, err := admin.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageShell("Admin", comp).Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	}
	mux.HandleFunc("GET /admin/{slug}", adminPage)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("admin_gorm listening on %s — open /admin", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
