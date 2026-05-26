package crud

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// DeriveGormCRUDTable wires CRUDTable[T] to a *gorm.DB. The closures
// auto-preload every declared association on detail reads (so the
// rendered dump shows full short-names), use ILIKE/LIKE search across the
// model's Searchable string columns, and after Save replay every
// many-to-many relation through Association().Replace() to persist the
// picker selections.
//
// The MetaModel is consulted as live state at call time: caller can
// post-mutate (RelatedCRUD, FormHelp, ...) and the next request will
// observe it.
func DeriveGormCRUDTable[T any](db *gorm.DB, mm MetaModel[T]) CRUDTable[T] {
	c := CRUDTable[T]{
		URLBase:       "/" + strings.ToLower(mm.Name),
		MetaData:      mm,
		CreateEnabled: true,
		EditEnabled:   true,
		DeleteEnabled: true,
		ListID:        "table_" + randSuffix(),
	}

	// Resolve the column names for Searchable fields once — GORM's naming
	// strategy may convert e.g. "DisplayName" to "display_name".
	sch, schErr := schema.Parse(new(T), &sync.Map{}, db.NamingStrategy)
	searchColumns := []string{}
	if schErr == nil {
		for _, mf := range mm.Fields {
			if !mf.Searchable {
				continue
			}
			if f := sch.LookUpField(mf.Name); f != nil {
				searchColumns = append(searchColumns, f.DBName)
			}
		}
	}

	sortColumn := func(name string) string {
		if name == "" || sch == nil {
			return name
		}
		if f := sch.LookUpField(name); f != nil {
			return f.DBName
		}
		return name
	}

	c.Get = func(ctx context.Context, id uint) (T, error) {
		var row T
		err := db.WithContext(ctx).
			Preload(clause.Associations).
			First(&row, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, ErrNotFound
		}
		return row, err
	}

	c.List = func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error) {
		q := db.WithContext(ctx).Model(new(T))

		if search != "" && len(searchColumns) > 0 {
			needle := "%" + escapeLike(search) + "%"
			conds := make([]string, len(searchColumns))
			args := make([]any, len(searchColumns))
			for i, col := range searchColumns {
				conds[i] = col + " LIKE ?"
				args[i] = needle
			}
			q = q.Where(strings.Join(conds, " OR "), args...)
		}

		var total int64
		if err := q.Count(&total).Error; err != nil {
			return nil, 0, err
		}

		if sortBy != "" {
			col := sortColumn(sortBy)
			dir := "ASC"
			if sortDesc {
				dir = "DESC"
			}
			q = q.Order(col + " " + dir)
		} else {
			q = q.Order("id ASC")
		}
		if offset > 0 {
			q = q.Offset(offset)
		}
		if limit > 0 {
			q = q.Limit(limit)
		}
		// Preload top-level associations on the list query too — relation
		// columns in the table would otherwise show "—" because the
		// relation slices/structs come back zero. The TableView is
		// usually small (PageSize ~10-20) so the extra joins are fine.
		q = q.Preload(clause.Associations)

		var rows []T
		if err := q.Find(&rows).Error; err != nil {
			return nil, 0, err
		}
		out := make([]CRUDSearchResult[T], len(rows))
		for i, r := range rows {
			out[i] = CRUDSearchResult[T]{
				ID:  idOf(r),
				Row: r,
			}
		}
		return out, total, nil
	}

	c.Create = func(ctx context.Context, data T) (uint, T, error) {
		// On create, GORM will also upsert the related rows referenced by
		// many2many slices (it inserts the join rows). We don't want it
		// re-creating Skill rows by accident — the form posts only IDs, so
		// each related struct has just ID populated. GORM's
		// FullSaveAssociations is off by default; the join rows are written
		// for non-zero IDs without touching the related table. Good.
		if err := db.WithContext(ctx).Create(&data).Error; err != nil {
			return 0, data, err
		}
		// Re-read to populate any has-many slices and pull fresh data.
		id := idOf(data)
		fresh, err := c.Get(ctx, id)
		if err != nil {
			return id, data, err
		}
		return id, fresh, nil
	}

	c.Update = func(ctx context.Context, id uint, data T) (T, error) {
		// Ensure the primary key on the bound struct matches the URL.
		setIDField(&data, id)

		// Plain Save updates columns but doesn't replace many2many
		// associations or has-many slices, so we do that explicitly for
		// the relation fields the form actually drives (m2m only —
		// has-many is read-only on the parent form). Wrap in a
		// transaction so a Replace failure rolls back the row update.
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(&data).Error; err != nil {
				return err
			}
			rv := reflect.ValueOf(&data).Elem()
			for _, mf := range mm.Fields {
				if mf.RelationKind != RelationMany2Many {
					continue
				}
				slice := rv.FieldByName(mf.Name)
				if !slice.IsValid() {
					continue
				}
				assoc := tx.Model(&data).Association(mf.Name)
				if assoc.Error != nil {
					return fmt.Errorf("association %s: %w", mf.Name, assoc.Error)
				}
				if err := assoc.Replace(slice.Interface()); err != nil {
					return fmt.Errorf("replace %s: %w", mf.Name, err)
				}
			}
			return nil
		})
		if err != nil {
			return data, err
		}
		// Re-read so the response reflects the persisted relations.
		fresh, err := c.Get(ctx, id)
		if err != nil {
			return data, err
		}
		return fresh, nil
	}

	c.Delete = func(ctx context.Context, id uint) error {
		// GORM's Delete on a primary-key target is a no-op if the row
		// doesn't exist; surface ErrNotFound so handlers can react.
		var row T
		err := db.WithContext(ctx).First(&row, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return db.WithContext(ctx).Delete(&row, id).Error
	}

	return c
}

// escapeLike escapes LIKE pattern metacharacters so user input doesn't
// turn an exact-text search into a wildcard match. SQLite uses the same
// rules as standard SQL: % and _ are wildcards, escape with the
// LIKE ... ESCAPE clause — but we keep it simple and just sanitize.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
