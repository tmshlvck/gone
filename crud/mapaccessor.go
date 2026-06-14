package crud

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// mapAccessor is the in-memory-map Accessor[T] over a caller-owned
// map[uint]T + mutex (the map and mutex stay the caller's so tests/apps can
// inspect them directly). Search is a case-insensitive substring match over
// the model's Searchable fields; sort uses reflection on the named field.
type mapAccessor[T any] struct {
	store      map[uint]T
	mu         *sync.RWMutex
	searchable []string // field names with Searchable=true
}

// MapAccessor builds an in-memory Accessor for T over store + mu. mm supplies
// which fields are searchable. If T has an exported integer "ID" field,
// Create/Update keep it in sync with the map key.
func MapAccessor[T any](mm MetaModel[T], store map[uint]T, mu *sync.RWMutex) Accessor[T] {
	var searchable []string
	for _, mf := range mm.Fields {
		if mf.Searchable {
			searchable = append(searchable, mf.Name)
		}
	}
	return &mapAccessor[T]{store: store, mu: mu, searchable: searchable}
}

func (a *mapAccessor[T]) Get(_ context.Context, id uint) (T, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	v, ok := a.store[id]
	if !ok {
		var z T
		return z, ErrNotFound
	}
	return v, nil
}

func (a *mapAccessor[T]) List(_ context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	all := make([]CRUDSearchResult[T], 0, len(a.store))
	for id, v := range a.store {
		all = append(all, CRUDSearchResult[T]{ID: id, Row: v})
	}

	if search != "" {
		needle := strings.ToLower(search)
		filtered := all[:0]
		for _, r := range all {
			if rowMatchesSearch(r.Row, a.searchable, needle) {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}

	if sortBy == "" {
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	} else {
		sort.Slice(all, func(i, j int) bool {
			less := compareFieldByName(all[i].Row, all[j].Row, sortBy)
			if sortDesc {
				return !less
			}
			return less
		})
	}

	total := int64(len(all))
	if offset >= len(all) {
		return nil, total, nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func (a *mapAccessor[T]) Create(_ context.Context, data T) (uint, T, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := nextID(a.store)
	setIDField(&data, id)
	a.store[id] = data
	return id, data, nil
}

func (a *mapAccessor[T]) Update(_ context.Context, id uint, data T) (T, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.store[id]; !ok {
		var z T
		return z, ErrNotFound
	}
	setIDField(&data, id)
	a.store[id] = data
	return data, nil
}

func (a *mapAccessor[T]) Delete(_ context.Context, id uint) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.store[id]; !ok {
		return ErrNotFound
	}
	delete(a.store, id)
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// Reflection helpers for the map backend's search / sort / ID, plus the
// shared ID setter (also used by the GORM backend).
// ──────────────────────────────────────────────────────────────────────────

func rowMatchesSearch[T any](row T, searchFields []string, needle string) bool {
	rv := reflect.ValueOf(row)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return false
	}
	for _, name := range searchFields {
		f := rv.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		if f.Kind() == reflect.String && strings.Contains(strings.ToLower(f.String()), needle) {
			return true
		}
	}
	return false
}

func compareFieldByName[T any](a, b T, field string) bool {
	av := fieldByName(a, field)
	bv := fieldByName(b, field)
	if !av.IsValid() || !bv.IsValid() {
		return false
	}
	return reflectLess(av, bv)
}

func fieldByName(v any, name string) reflect.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return rv.FieldByName(name)
}

func reflectLess(a, b reflect.Value) bool {
	if a.Type() == reflect.TypeOf(time.Time{}) {
		return a.Interface().(time.Time).Before(b.Interface().(time.Time))
	}
	switch a.Kind() {
	case reflect.String:
		return a.String() < b.String()
	case reflect.Bool:
		return !a.Bool() && b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() < b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return a.Uint() < b.Uint()
	case reflect.Float32, reflect.Float64:
		return a.Float() < b.Float()
	}
	return false
}

func nextID[T any](store map[uint]T) uint {
	var n uint = 1
	for id := range store {
		if id >= n {
			n = id + 1
		}
	}
	return n
}

// setIDField writes id into the exported integer "ID" field of *data, if any.
// Shared by the map and GORM backends.
func setIDField[T any](data *T, id uint) {
	rv := reflect.ValueOf(data).Elem()
	if rv.Kind() != reflect.Struct {
		return
	}
	f := rv.FieldByName("ID")
	if !f.IsValid() || !f.CanSet() {
		return
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(uint64(id))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(int64(id))
	}
}
