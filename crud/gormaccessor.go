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

// gormAccessor is the GORM-backed Accessor[T]. It auto-preloads every
// declared association on reads (so rendered dumps and relation cells show
// full short-labels), searches across the model's Searchable columns with
// LIKE, and on update replays each many-to-many relation through
// Association().Replace() to persist the picker selections.
type gormAccessor[T any] struct {
	db            *gorm.DB
	searchColumns []string            // DB column names of Searchable string fields
	sortColumn    func(string) string // MetaField name → DB column name
	m2mFields     []string            // Go field names of many2many relations
}

// GORMAccessor builds a GORM-backed Accessor for T. mm supplies which fields
// are searchable/sortable and which are many-to-many (resolved to columns
// once, here, via GORM's naming strategy).
func GORMAccessor[T any](mm MetaModel[T], db *gorm.DB) Accessor[T] {
	a := &gormAccessor[T]{db: db}

	sch, schErr := schema.Parse(new(T), &sync.Map{}, db.NamingStrategy)
	if schErr == nil {
		for _, mf := range mm.Fields {
			if mf.Searchable {
				if f := sch.LookUpField(mf.Name); f != nil {
					a.searchColumns = append(a.searchColumns, f.DBName)
				}
			}
		}
	}
	a.sortColumn = func(name string) string {
		if name == "" || sch == nil {
			return name
		}
		if f := sch.LookUpField(name); f != nil {
			return f.DBName
		}
		return name
	}
	for _, mf := range mm.Fields {
		if mf.RelationKind == RelationMany2Many {
			a.m2mFields = append(a.m2mFields, mf.Name)
		}
	}
	return a
}

func (a *gormAccessor[T]) Get(ctx context.Context, id uint) (T, error) {
	var row T
	err := a.db.WithContext(ctx).
		Preload(clause.Associations).
		First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrNotFound
	}
	return row, err
}

func (a *gormAccessor[T]) List(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error) {
	q := a.db.WithContext(ctx).Model(new(T))

	if search != "" && len(a.searchColumns) > 0 {
		needle := "%" + escapeLike(search) + "%"
		conds := make([]string, len(a.searchColumns))
		args := make([]any, len(a.searchColumns))
		for i, col := range a.searchColumns {
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
		dir := "ASC"
		if sortDesc {
			dir = "DESC"
		}
		q = q.Order(a.sortColumn(sortBy) + " " + dir)
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
	// columns would otherwise show "—". The page is small (PageSize ~10-20)
	// so the extra joins are fine.
	q = q.Preload(clause.Associations)

	var rows []T
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]CRUDSearchResult[T], len(rows))
	for i, r := range rows {
		out[i] = CRUDSearchResult[T]{ID: idOf(r), Row: r}
	}
	return out, total, nil
}

func (a *gormAccessor[T]) Create(ctx context.Context, data T) (uint, T, error) {
	// GORM writes the join rows for non-zero related IDs without re-creating
	// the related rows (FullSaveAssociations is off), which is what we want:
	// the form posts only IDs.
	if err := a.db.WithContext(ctx).Create(&data).Error; err != nil {
		return 0, data, err
	}
	id := idOf(data)
	fresh, err := a.Get(ctx, id) // re-read to populate has-many slices
	if err != nil {
		return id, data, err
	}
	return id, fresh, nil
}

func (a *gormAccessor[T]) Update(ctx context.Context, id uint, data T) (T, error) {
	setIDField(&data, id) // ensure the PK matches the URL

	// Save updates columns but doesn't replace m2m associations, so replay
	// those explicitly (has-many is read-only on the parent form). Wrap in a
	// transaction so a Replace failure rolls back the row update.
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&data).Error; err != nil {
			return err
		}
		rv := reflect.ValueOf(&data).Elem()
		for _, name := range a.m2mFields {
			slice := rv.FieldByName(name)
			if !slice.IsValid() {
				continue
			}
			assoc := tx.Model(&data).Association(name)
			if assoc.Error != nil {
				return fmt.Errorf("association %s: %w", name, assoc.Error)
			}
			if err := assoc.Replace(slice.Interface()); err != nil {
				return fmt.Errorf("replace %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return data, err
	}
	fresh, err := a.Get(ctx, id) // re-read so the response reflects persisted relations
	if err != nil {
		return data, err
	}
	return fresh, nil
}

func (a *gormAccessor[T]) Delete(ctx context.Context, id uint) error {
	var row T
	err := a.db.WithContext(ctx).First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return a.db.WithContext(ctx).Delete(&row, id).Error
}

// escapeLike escapes LIKE metacharacters so user input doesn't turn an
// exact-text search into a wildcard match.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
