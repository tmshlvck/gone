package site

import (
	"reflect"
	"time"

	"gorm.io/gorm"
)

var timeType = reflect.TypeOf(time.Time{})

// ForceUTC makes a GORM database store every time.Time in UTC, on any
// backend (SQLite, Postgres, …). It is the storage-layer guarantee the
// rest of gone's time handling rests on: values at rest are always UTC,
// so SQL ordering, range filters, and comparisons operate on the
// instant — and per-session timezone display becomes purely a
// presentation concern layered on top.
//
// Call it once, right after gorm.Open and before any writes:
//
//	db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
//	if err := site.ForceUTC(db); err != nil { log.Fatal(err) }
//
// It does two complementary things, because one alone is not enough:
//
//   - Wraps db.NowFunc so GORM's automatic CreatedAt / UpdatedAt fields
//     are generated in UTC. GORM assigns those from NowFunc deep inside
//     the create/update SQL build — after the callbacks below run — so
//     NowFunc is the only place to catch them. An existing NowFunc (e.g.
//     a fixed test clock) is preserved and merely forced to UTC.
//   - Registers before-create and before-update callbacks that convert
//     any explicitly-set time.Time / *time.Time field on the model to
//     UTC before it is written — covering app-assigned values such as
//     time.Now() (which returns time.Local!), times parsed in another
//     zone, or data from external sources.
//
// Why it matters: a time.Time carrying a non-UTC location is otherwise
// stored with that offset. SQLite keeps the literal text (e.g.
// "2024-06-15T14:30:00+02:00"), so two rows that are the same instant
// in different zones sort and range-filter by their wall-clock string
// rather than their instant — a silent correctness bug. Postgres
// timestamptz columns normalize the instant regardless, but
// timestamp-without-time-zone columns share the hazard. ForceUTC
// removes it uniformly.
//
// Call once per *gorm.DB; re-registering the named callbacks on the
// same handle returns an error.
func ForceUTC(db *gorm.DB) error {
	prev := db.Config.NowFunc
	db.Config.NowFunc = func() time.Time {
		if prev != nil {
			return prev().UTC()
		}
		return time.Now().UTC()
	}
	if err := db.Callback().Create().Before("gorm:create").
		Register("gone:force_utc_create", forceUTCWrite); err != nil {
		return err
	}
	return db.Callback().Update().Before("gorm:update").
		Register("gone:force_utc_update", forceUTCWrite)
}

// forceUTCWrite normalizes every time.Time / *time.Time field on the
// record(s) being written to UTC. Handles single-row and batch
// (slice) statements.
func forceUTCWrite(tx *gorm.DB) {
	if tx.Statement.Schema == nil {
		return
	}
	rv := tx.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			utcifyRecord(tx, rv.Index(i))
		}
	case reflect.Struct:
		utcifyRecord(tx, rv)
	}
}

func utcifyRecord(tx *gorm.DB, rv reflect.Value) {
	ctx := tx.Statement.Context
	for _, f := range tx.Statement.Schema.Fields {
		// IndirectFieldType is the dereferenced type, so this matches
		// both time.Time and *time.Time fields.
		if f.IndirectFieldType != timeType {
			continue
		}
		v, isZero := f.ValueOf(ctx, rv)
		if isZero {
			continue
		}
		switch t := v.(type) {
		case time.Time:
			if t.Location() != time.UTC {
				_ = f.Set(ctx, rv, t.UTC())
			}
		case *time.Time:
			if t != nil && t.Location() != time.UTC {
				u := t.UTC()
				_ = f.Set(ctx, rv, &u)
			}
		}
	}
}
