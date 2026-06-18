package crud

import "context"

// ChangeKind identifies which mutation produced a ChangeEvent.
type ChangeKind int

const (
	ChangeCreate ChangeKind = iota
	ChangeUpdate
	ChangeDelete
	// ChangeRead and ChangeList are read events — emitted only when the
	// accessor was built with ObserveReads. They exist for audit trails
	// ("who looked at row 42"); see ObserveReads for the volume caveat.
	ChangeRead
	ChangeList
)

// String renders the kind as "create" / "update" / "delete" / "read" / "list"
// (anything else as "unknown") — handy for log lines and test failures.
func (k ChangeKind) String() string {
	switch k {
	case ChangeCreate:
		return "create"
	case ChangeUpdate:
		return "update"
	case ChangeDelete:
		return "delete"
	case ChangeRead:
		return "read"
	case ChangeList:
		return "list"
	default:
		return "unknown"
	}
}

// ChangeEvent describes a single successful operation observed on an Accessor.
// What's populated depends on Kind:
//
//   - ChangeCreate / ChangeUpdate: ID and Row are the resulting row.
//   - ChangeDelete: ID is set; Row is the zero value unless the accessor was
//     built with ObserveDeletes (which re-reads the row before deleting it).
//   - ChangeRead: ID and Row are the fetched row (read events; ObserveReads).
//   - ChangeList: a list query ran — ID is 0 and Row is the zero value (a
//     list has no single id). Count carries how many rows the page returned;
//     the per-row identities aren't enumerated.
type ChangeEvent[T any] struct {
	Kind ChangeKind
	ID   uint
	Row  T
	// Count is the number of rows returned, set for ChangeList only (0 for
	// every other kind).
	Count int
}

// observed wraps an Accessor[T] so on(ctx, event) fires after each successful
// Create/Update/Delete. It is the single chokepoint every mutation path runs
// through — the table's create/edit/delete handlers AND CSV import all go via
// Data — so one wrap captures them all.
type observed[T any] struct {
	inner Accessor[T]
	on    func(context.Context, ChangeEvent[T])
	// preReadDelete makes Delete fetch the row first so the emitted event
	// carries its contents instead of the zero value. Off by default — it
	// costs an extra read.
	preReadDelete bool
	// observeReads turns on ChangeRead/ChangeList emission for Get/List. Off
	// by default: reads are far higher volume than writes (List fires on
	// every render/search/sort), so they're opt-in.
	observeReads bool
}

// ObserveAccessor wraps inner so that on(ctx, event) fires after each
// successful Create/Update/Delete. The event carries the kind, the row ID, and
// (for Create/Update) the resulting row.
//
// on runs synchronously inside the request goroutine, after the underlying
// write returns (so the row is already committed). It MUST NOT block — a
// typical use sends to a buffered channel with a select/default fallback:
//
//	updates := make(chan crud.ChangeEvent[Hero], 64)
//	data := crud.ObserveAccessor(
//		crud.GORMAccessor[Hero](mm, db),
//		func(ctx context.Context, e crud.ChangeEvent[Hero]) {
//			select {
//			case updates <- e:
//			default: // nobody draining — drop rather than stall the request
//			}
//		},
//	)
//	table := crud.NewTable(mm, data, site.DefaultSettings{}, az)
//
// A failed mutation fires nothing. Delete events carry a zero Row unless the
// accessor was built with ObserveDeletes.
func ObserveAccessor[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T] {
	return &observed[T]{inner: inner, on: on}
}

// ObserveDeletes is ObserveAccessor with one extra guarantee: Delete events
// carry the row that was removed (the accessor re-reads it before deleting).
// Use it when subscribers need the old contents — at the cost of one extra
// Get per delete.
func ObserveDeletes[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T] {
	return &observed[T]{inner: inner, on: on, preReadDelete: true}
}

// ObserveReads is ObserveAccessor that ALSO fires on reads: ChangeRead for each
// Get, ChangeList for each List. Use it for a full audit trail ("who viewed row
// 42").
//
// Mind the volume: List runs on every table render, search keystroke, sort, and
// page change, so this can emit far more events than the write path. Make the
// callback cheap, and consider sampling or filtering by Kind inside it (e.g.
// record ChangeRead but skip ChangeList). For non-HTTP callers (CSV export,
// background jobs) the ctx carries no session — audit those as anonymous.
func ObserveReads[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T] {
	return &observed[T]{inner: inner, on: on, preReadDelete: true, observeReads: true}
}

func (a *observed[T]) Get(ctx context.Context, id uint) (T, error) {
	row, err := a.inner.Get(ctx, id)
	if err != nil {
		return row, err
	}
	if a.observeReads {
		a.emit(ctx, ChangeEvent[T]{Kind: ChangeRead, ID: id, Row: row})
	}
	return row, nil
}

func (a *observed[T]) List(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error) {
	results, total, err := a.inner.List(ctx, search, sortBy, sortDesc, offset, limit)
	if err != nil {
		return results, total, err
	}
	if a.observeReads {
		a.emit(ctx, ChangeEvent[T]{Kind: ChangeList, Count: len(results)})
	}
	return results, total, nil
}

func (a *observed[T]) Create(ctx context.Context, in T) (uint, T, error) {
	id, out, err := a.inner.Create(ctx, in)
	if err != nil {
		return id, out, err
	}
	a.emit(ctx, ChangeEvent[T]{Kind: ChangeCreate, ID: id, Row: out})
	return id, out, nil
}

func (a *observed[T]) Update(ctx context.Context, id uint, in T) (T, error) {
	out, err := a.inner.Update(ctx, id, in)
	if err != nil {
		return out, err
	}
	a.emit(ctx, ChangeEvent[T]{Kind: ChangeUpdate, ID: id, Row: out})
	return out, nil
}

func (a *observed[T]) Delete(ctx context.Context, id uint) error {
	// Capture the row first when asked — once it's deleted we can't read it.
	// A read failure is non-fatal to the delete; we just emit a zero Row.
	var row T
	if a.preReadDelete {
		row, _ = a.inner.Get(ctx, id)
	}
	if err := a.inner.Delete(ctx, id); err != nil {
		return err
	}
	a.emit(ctx, ChangeEvent[T]{Kind: ChangeDelete, ID: id, Row: row})
	return nil
}

// emit fires the callback if one was supplied. Centralized so a nil callback
// is tolerated (ObserveAccessor with on==nil degrades to a transparent
// pass-through rather than panicking).
func (a *observed[T]) emit(ctx context.Context, e ChangeEvent[T]) {
	if a.on != nil {
		a.on(ctx, e)
	}
}
