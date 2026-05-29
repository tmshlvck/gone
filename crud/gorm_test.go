package crud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type gormHero struct {
	ID     uint
	Name   string
	Realm  string
	Power  int
	Active bool
	Skills []gormSkill `gorm:"many2many:gorm_hero_skills"`
}

type gormSkill struct {
	ID    uint
	Name  string
	Level int
}

// newGormServer spins up an in-memory SQLite-backed CRUDTable for gormHero
// with a seeded skill list and three heroes. Returns the mux, the table,
// and the underlying db for direct assertions.
func newGormServer(t *testing.T) (*http.ServeMux, *CRUDTable[gormHero], *CRUDTable[gormSkill], *gorm.DB) {
	t.Helper()
	// Per-test database — share by giving each test its own file id so
	// parallel tests don't collide. shared cache lets every conn see it.
	dsn := "file:" + randSuffix() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&gormHero{}, &gormSkill{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	skills := []gormSkill{
		{Name: "Swordsmanship", Level: 8},
		{Name: "Archery", Level: 7},
	}
	if err := db.Create(&skills).Error; err != nil {
		t.Fatalf("seed skills: %v", err)
	}
	heroes := []gormHero{
		{Name: "Aragorn", Realm: "Gondor", Power: 90, Active: true, Skills: []gormSkill{skills[0]}},
		{Name: "Legolas", Realm: "Mirkwood", Power: 85, Active: true, Skills: []gormSkill{skills[1]}},
		{Name: "Gandalf", Realm: "Middle-earth", Power: 99, Active: true},
	}
	if err := db.Create(&heroes).Error; err != nil {
		t.Fatalf("seed heroes: %v", err)
	}

	hmm, err := DeriveMetaModel[gormHero]()
	if err != nil {
		t.Fatalf("derive hero: %v", err)
	}
	smm, err := DeriveMetaModel[gormSkill]()
	if err != nil {
		t.Fatalf("derive skill: %v", err)
	}
	htbl := DeriveGormCRUDTable[gormHero](hmm, nil, db)
	stbl := DeriveGormCRUDTable[gormSkill](smm, nil, db)
	htbl.Slug = "heroes"
	stbl.Slug = "skills"

	// Wire the m2m relation to Skill.
	for i := range htbl.MetaData.Fields {
		if htbl.MetaData.Fields[i].Name == "Skills" {
			htbl.MetaData.Fields[i].RelatedCRUD = &stbl
		}
	}

	mux := http.NewServeMux()
	if _, err := htbl.Route(mux, "", nil); err != nil {
		t.Fatalf("route hero: %v", err)
	}
	if _, err := stbl.Route(mux, "", nil); err != nil {
		t.Fatalf("route skill: %v", err)
	}
	mux.HandleFunc("GET "+htbl.URLBase(), func(w http.ResponseWriter, r *http.Request) {
		comp, err := htbl.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = comp.Render(r.Context(), w)
	})
	return mux, &htbl, &stbl, db
}

// keep sync import used (avoid unused-import flake if helper changes)
var _ = sync.RWMutex{}

func TestGorm_ListReturnsSeededRows(t *testing.T) {
	mux, _, _, _ := newGormServer(t)
	code, body := get(t, mux, "/heroes")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, name := range []string{"Aragorn", "Legolas", "Gandalf"} {
		if !strings.Contains(body, name) {
			t.Errorf("missing %q in list body", name)
		}
	}
}

func TestGorm_Search(t *testing.T) {
	mux, _, _, _ := newGormServer(t)
	_, body := get(t, mux, "/heroes?q=ara")
	if !strings.Contains(body, "Aragorn") {
		t.Error("Aragorn should match ?q=ara")
	}
	if strings.Contains(body, "Legolas") {
		t.Error("Legolas should NOT match ?q=ara")
	}
}

func TestGorm_CreatePersists(t *testing.T) {
	mux, hbl, _, _ := newGormServer(t)
	rec := postForm(t, mux, "/heroes/create", "Name=Frodo&Realm=Shire&Power=42")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", rec.Code)
	}
	rows, total, err := hbl.List(context.Background(), "Frodo", "", false, 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || rows[0].Row.Name != "Frodo" {
		t.Errorf("Frodo not persisted: total=%d rows=%+v", total, rows)
	}
}

func TestGorm_UpdateReplacesM2M(t *testing.T) {
	mux, hbl, stbl, _ := newGormServer(t)
	// Look up Aragorn's row and the skill IDs.
	rows, _, err := hbl.List(context.Background(), "Aragorn", "", false, 0, 1)
	if err != nil || len(rows) == 0 {
		t.Fatalf("seed lookup: %v rows=%d", err, len(rows))
	}
	aragornID := rows[0].ID
	skillRows, _, err := stbl.List(context.Background(), "Archery", "", false, 0, 1)
	if err != nil || len(skillRows) == 0 {
		t.Fatalf("skill lookup: %v", err)
	}
	archeryID := skillRows[0].ID

	// POST an edit that swaps Aragorn's m2m skills to just "Archery".
	body := encodeFormBody(map[string][]string{
		"Name":   {"Aragorn"},
		"Realm":  {"Gondor"},
		"Power":  {"90"},
		"Active": {"on"},
		"Skills": {strconvU(archeryID)},
	})
	rec := postForm(t, mux, "/heroes/"+strconvU(aragornID)+"/edit", body)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := hbl.Get(context.Background(), aragornID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "Archery" {
		t.Errorf("expected Skills=[Archery], got %+v", got.Skills)
	}
}

func TestGorm_Delete(t *testing.T) {
	mux, hbl, _, _ := newGormServer(t)
	rows, _, _ := hbl.List(context.Background(), "Gandalf", "", false, 0, 1)
	if len(rows) == 0 {
		t.Fatalf("Gandalf not seeded")
	}
	id := rows[0].ID
	rec := postForm(t, mux, "/heroes/"+strconvU(id)+"/delete", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := hbl.Get(context.Background(), id); err == nil {
		t.Error("Gandalf should be gone")
	}
}

func TestGorm_RowDisplayBareboneFragment(t *testing.T) {
	mux, hbl, _, _ := newGormServer(t)
	rows, _, _ := hbl.List(context.Background(), "Aragorn", "", false, 0, 1)
	if len(rows) == 0 {
		t.Fatal("Aragorn not seeded")
	}
	id := rows[0].ID
	code, body := get(t, mux, "/heroes/"+strconvU(id)+"/display")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, forbidden := range []string{"<html", "<body"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/display must not include %q", forbidden)
		}
	}
	if !strings.Contains(body, "Aragorn") {
		t.Errorf("/display missing Aragorn body=%s", body)
	}
	if !strings.Contains(body, "Swordsmanship") {
		t.Errorf("/display should include preloaded skill name; got %s", body)
	}
}

// encodeFormBody builds an x-www-form-urlencoded body from a map;
// values for the same key emit one k=v pair each.
func encodeFormBody(m map[string][]string) string {
	var sb strings.Builder
	first := true
	for k, vs := range m {
		for _, v := range vs {
			if !first {
				sb.WriteByte('&')
			}
			first = false
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(urlEscape(v))
		}
	}
	return sb.String()
}

func urlEscape(s string) string {
	// Minimal — test inputs are alnum/spaces.
	r := strings.NewReplacer(" ", "+", "&", "%26", "=", "%3D")
	return r.Replace(s)
}

func strconvU(u uint) string {
	const digits = "0123456789"
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = digits[u%10]
		u /= 10
	}
	return string(buf[i:])
}

// keep httptest import alive when only used via the get/postForm helpers.
var _ = httptest.NewRequest
