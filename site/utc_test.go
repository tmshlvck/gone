package site

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type utcModel struct {
	ID        uint
	Name      string
	Forged    time.Time  // explicit value field
	Retired   *time.Time // explicit pointer field
	CreatedAt time.Time  // GORM auto timestamp (from NowFunc)
	UpdatedAt time.Time  // GORM auto timestamp (from NowFunc)
}

func openUTC(t *testing.T, dsn string, force bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&utcModel{}); err != nil {
		t.Fatal(err)
	}
	if force {
		if err := ForceUTC(db); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func rawCol(t *testing.T, db *gorm.DB, col, name string) string {
	t.Helper()
	var s string
	if err := db.Raw("SELECT "+col+" FROM utc_models WHERE name = ?", name).Scan(&s).Error; err != nil {
		t.Fatal(err)
	}
	return s
}

// With ForceUTC, explicit non-UTC time fields, pointer fields, and the
// auto CreatedAt/UpdatedAt are all stored as UTC ("...Z").
func TestForceUTC_StoresUTC(t *testing.T) {
	db := openUTC(t, "file:utc_on?mode=memory&cache=shared", true)
	prague, _ := time.LoadLocation("Europe/Prague")
	ret := time.Date(2024, 3, 1, 9, 0, 0, 0, prague) // 08:00Z
	row := utcModel{
		Name:    "x",
		Forged:  time.Date(2024, 6, 15, 16, 0, 0, 0, prague), // 14:00Z
		Retired: &ret,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ col, want string }{
		{"forged", "2024-06-15T14:00:00Z"},
		{"retired", "2024-03-01T08:00:00Z"},
	} {
		if got := rawCol(t, db, tc.col, "x"); got != tc.want {
			t.Errorf("%s stored %q, want %q", tc.col, got, tc.want)
		}
	}
	// Auto timestamp must be UTC (ends in Z, not an offset).
	if got := rawCol(t, db, "created_at", "x"); got == "" || got[len(got)-1] != 'Z' {
		t.Errorf("created_at stored %q, want a UTC (...Z) value", got)
	}
}

// Sanity: the in-Go value comes back as the same instant.
func TestForceUTC_InstantPreserved(t *testing.T) {
	db := openUTC(t, "file:utc_instant?mode=memory&cache=shared", true)
	prague, _ := time.LoadLocation("Europe/Prague")
	in := time.Date(2024, 6, 15, 16, 0, 0, 0, prague)
	db.Create(&utcModel{Name: "y", Forged: in})

	var got utcModel
	db.Where("name = ?", "y").First(&got)
	if !got.Forged.Equal(in) {
		t.Errorf("read back %v, not the same instant as %v", got.Forged, in)
	}
	if got.Forged.Location() != time.UTC {
		t.Errorf("read-back location = %v, want UTC", got.Forged.Location())
	}
}

// Regression for the actual bug: without ForceUTC, mixed offsets sort
// by wall-clock text (wrong); with ForceUTC they sort by instant.
func TestForceUTC_FixesSort(t *testing.T) {
	cest := time.FixedZone("CEST", 2*3600)
	base := time.Date(2024, 6, 15, 12, 30, 0, 0, time.UTC)
	later := base                            // 12:30Z
	earlier := base.Add(-time.Hour).In(cest) // 13:30+02:00 == 11:30Z (earlier instant)

	t.Run("without ForceUTC mis-sorts", func(t *testing.T) {
		db := openUTC(t, "file:utc_off?mode=memory&cache=shared", false)
		db.Create(&utcModel{Name: "later", Forged: later})
		db.Create(&utcModel{Name: "earlier", Forged: earlier})
		var names []string
		db.Raw("SELECT name FROM utc_models ORDER BY forged").Scan(&names)
		if names[0] == "earlier" {
			t.Skip("driver happened to normalize; the hazard is offset-dependent")
		}
		// Documents the bug: text sort puts the genuinely-earlier row last.
		if names[0] != "later" {
			t.Fatalf("unexpected order %v", names)
		}
	})

	t.Run("with ForceUTC sorts by instant", func(t *testing.T) {
		db := openUTC(t, "file:utc_fixed?mode=memory&cache=shared", true)
		db.Create(&utcModel{Name: "later", Forged: later})
		db.Create(&utcModel{Name: "earlier", Forged: earlier})
		var names []string
		db.Raw("SELECT name FROM utc_models ORDER BY forged").Scan(&names)
		if names[0] != "earlier" || names[1] != "later" {
			t.Fatalf("ORDER BY forged = %v, want [earlier later]", names)
		}
	})
}
