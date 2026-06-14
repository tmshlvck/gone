package crud

import (
	"context"
	"errors"
)

// Accessor is the data plane of a CRUDTable: the five operations a table
// performs on its rows. A backend implements it (MapAccessor, GORMAccessor,
// or any custom type — e.g. a safe view over a model whose own type
// shouldn't be written directly); the rendering, validation, and routing
// code is backend-blind.
//
// List returns the page of rows matching search/sort plus the unpaged total.
// Get/Update/Delete return ErrNotFound for an unknown id.
type Accessor[T any] interface {
	Get(ctx context.Context, id uint) (T, error)
	List(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
	Create(ctx context.Context, in T) (id uint, out T, err error)
	Update(ctx context.Context, id uint, in T) (out T, err error)
	Delete(ctx context.Context, id uint) error
}

// CRUDSearchResult bundles a row with the ID the backend assigned to it.
// The ID is exposed separately so handlers don't have to dig into the
// model to discover it.
type CRUDSearchResult[T any] struct {
	ID  uint
	Row T
}

// ErrNotFound is returned by Get/Update/Delete when the id is unknown.
var ErrNotFound = errors.New("not found")
