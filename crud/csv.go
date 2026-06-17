package crud

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// CSV import / export for a CRUDTable. The wire format is one header row of
// field names plus a leading "ID" column, then one row per record; scalar
// cell values use the same plain-text stringification as form pre-fill
// (formatValue), and import feeds each present column back through the field's
// own BindStrings + validation so the normal pipeline runs unchanged.
//
// Export and import are deliberately NOT symmetric. Export carries every
// non-secret, non-internal column — including read-only ones (timestamps,
// computed fields, the has-many inverse) as reference data. Import only writes
// the bindable columns; read-only / has-many columns in a file are recognized
// as "unknown" and ignored, so an exported file re-imports cleanly without the
// read-only columns fighting the data.
//
// Relations:
//   - RelationSingle round-trips as the FK id under its FormFieldName column
//     (e.g. "OwnerID"); 0/blank clears it.
//   - RelationMany2Many round-trips as a single cell holding a csvListSep-
//     separated list of related ids (e.g. "5;8;12"); blank clears the set.
//   - RelationHasMany (read-only inverse side) is EXPORTED as the same id list
//     for reference, but ignored on import (its bind hook is a no-op).
//
// Import is a PATCH, not a full overwrite: only columns present in the header
// are bound, so a partial CSV updates just those fields and leaves the rest of
// the row untouched. Times are UTC wall clock (the at-rest representation), not
// session-zone-reinterpreted the way the edit form is.

// csvIDColumn is the dedicated leading column carrying CRUDSearchResult.ID.
// It's handled out-of-band (not via a MetaField) because the id lives on the
// accessor result, and on import it drives upsert: present & non-blank →
// Update, blank/absent → Create.
const csvIDColumn = "ID"

// csvListSep separates related ids inside a single many-to-many cell. A
// semicolon avoids clashing with CSV's own comma and stays spreadsheet-
// friendly (no quoting needed for plain id lists).
const csvListSep = ";"

// csvImportFields returns the fields CSV import binds: every writable field.
//
//   - Hidden — excluded. These are internal columns (notably a single
//     relation's raw FK scalar, e.g. "OwnerID", which the relation field
//     itself already carries) that shouldn't be hand-set by name.
//   - ReadOnly — excluded. Nothing to write. This is also what excludes a
//     RelationHasMany: derive always marks the has-many inverse ReadOnly, and
//     its bind hook is a no-op, so it can't and shouldn't be imported.
//   - The "ID" primary key — excluded as a field; it's carried out-of-band by
//     the dedicated leading column that drives upsert.
//   - NoExport fields are STILL included (NoExport gates export only).
//
// By relation kind: RelationSingle and RelationMany2Many are writable, so they
// stay; RelationHasMany is read-only, so it's dropped (via the ReadOnly check).
// Order is declaration order.
func csvImportFields[T any](mm MetaModel[T]) []MetaField {
	var out []MetaField
	for _, mf := range mm.Fields {
		if mf.Hidden || mf.ReadOnly || strings.EqualFold(mf.Name, csvIDColumn) {
			continue
		}
		out = append(out, mf)
	}
	return out
}

// csvExportFields returns the columns CSV export emits: every non-secret,
// non-internal field. It is INTENTIONALLY broader than csvImportFields —
// read-only columns are exported as reference data (and ignored on re-import).
//
//   - Hidden — excluded (internal columns; a single relation's raw FK scalar
//     would otherwise duplicate the relation's own "OwnerID" column).
//   - NoExport — excluded (secrets must never leave in a dump).
//   - The "ID" field — excluded; carried by the dedicated leading column.
//   - ReadOnly fields (timestamps, computed values) — INCLUDED, for reference.
//
// By relation kind: RelationSingle → its FK id column; RelationMany2Many → an
// id-list cell; RelationHasMany → an id-list cell too (read-only reference),
// since it's not Hidden and the inverse ids are useful to see.
func csvExportFields[T any](mm MetaModel[T]) []MetaField {
	var out []MetaField
	for _, mf := range mm.Fields {
		if mf.Hidden || mf.NoExport || strings.EqualFold(mf.Name, csvIDColumn) {
			continue
		}
		out = append(out, mf)
	}
	return out
}

// csvColKey is the header / form key for a field: its FormFieldName (which is
// the FK name for a single relation, e.g. "OwnerID", and the field name
// otherwise). Falls back to Name if unset.
func csvColKey(mf MetaField) string {
	if mf.FormFieldName != "" {
		return mf.FormFieldName
	}
	return mf.Name
}

// csvColumnNames returns the importable column names (ID first, then each
// import field's key) — shown to the user as the recognized-columns hint.
func csvColumnNames[T any](mm MetaModel[T]) []string {
	fields := csvImportFields(mm)
	names := make([]string, 0, len(fields)+1)
	names = append(names, csvIDColumn)
	for _, mf := range fields {
		names = append(names, csvColKey(mf))
	}
	return names
}

// csvExportCell stringifies one field of a row for export. Relations emit ids
// (single → the FK; many2many and has-many → a csvListSep id list); scalars
// use formatValue.
func csvExportCell(mf MetaField, rv reflect.Value) string {
	switch mf.RelationKind {
	case RelationSingle:
		if mf.FKFieldName == "" {
			return ""
		}
		fk := rv.FieldByName(mf.FKFieldName)
		if fk.IsValid() && fk.CanUint() {
			if id := fk.Uint(); id != 0 {
				return strconv.FormatUint(id, 10)
			}
		}
		return ""
	case RelationMany2Many, RelationHasMany:
		f := rv.FieldByName(mf.Name)
		if !f.IsValid() || f.Kind() != reflect.Slice {
			return ""
		}
		ids := make([]string, 0, f.Len())
		for i := 0; i < f.Len(); i++ {
			el := f.Index(i)
			for el.Kind() == reflect.Pointer {
				el = el.Elem()
			}
			idF := el.FieldByName("ID")
			if idF.IsValid() && idF.CanUint() {
				if id := idF.Uint(); id != 0 {
					ids = append(ids, strconv.FormatUint(id, 10))
				}
			}
		}
		return strings.Join(ids, csvListSep)
	default:
		fv := rv.FieldByName(mf.Name)
		if !fv.IsValid() {
			return ""
		}
		return formatValue(mf, fv.Interface())
	}
}

// writeCSV streams results to w as CSV: a header row (ID + field keys) then
// one row per result.
func writeCSV[T any](w io.Writer, mm MetaModel[T], results []CRUDSearchResult[T]) error {
	cw := csv.NewWriter(w)
	fields := csvExportFields(mm)

	header := make([]string, 0, len(fields)+1)
	header = append(header, csvIDColumn)
	for _, mf := range fields {
		header = append(header, csvColKey(mf))
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, res := range results {
		rv := reflect.ValueOf(res.Row)
		for rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		rec := make([]string, 0, len(fields)+1)
		rec = append(rec, strconv.FormatUint(uint64(res.ID), 10))
		for _, mf := range fields {
			rec = append(rec, csvExportCell(mf, rv))
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// plannedUpsert is one parsed, validated, not-yet-persisted row: id==0 means
// create, non-zero means update that id.
type plannedUpsert[T any] struct {
	id  uint
	row T
}

// csvCellToForm turns one CSV cell into the wire values the field's BindStrings
// expects:
//   - many2many: split the csvListSep list into individual ids;
//   - checkbox: DefaultBindStrings reads the "on" sentinel (not "true"), so a
//     truthy cell becomes ["on"] and anything else is left absent (→ false);
//   - everything else (scalars, single relation): the trimmed cell verbatim.
func csvCellToForm(mf MetaField, cell string) []string {
	if mf.RelationKind == RelationMany2Many {
		parts := strings.Split(cell, csvListSep)
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if mf.FormInputType == "checkbox" {
		switch strings.ToLower(strings.TrimSpace(cell)) {
		case "1", "t", "y", "on", "yes", "true":
			return []string{"on"}
		default:
			return nil
		}
	}
	return []string{strings.TrimSpace(cell)}
}

// bindCSVRow binds the present columns of one CSV row onto out, leaving fields
// whose column is absent untouched (PATCH semantics — a partial CSV doesn't
// wipe omitted fields, and on update the fetched row keeps its current values).
// It mirrors DefaultBindForm's per-field validate + model-level Validate, but
// over csvImportFields and gated on column presence. present[key] reports
// whether that column appeared in the file.
func bindCSVRow[T any](mm MetaModel[T], form map[string][]string, present map[string]bool, out *T) error {
	verrs := ValidationErrors{}
	rv := reflect.ValueOf(out).Elem()
	for _, mf := range csvImportFields(mm) {
		key := csvColKey(mf)
		if !present[key] {
			continue // column absent → leave the field as-is
		}
		if err := mf.BindStrings(mf, form[key], out); err != nil {
			verrs[mf.Name] = err.Error()
			continue
		}
		if mf.FieldValidate != nil {
			fv := rv.FieldByName(mf.Name)
			if fv.IsValid() {
				if err := mf.FieldValidate(fv.Interface()); err != nil {
					verrs[mf.Name] = err.Error()
				}
			}
		}
	}
	if len(verrs) == 0 && mm.Validate != nil {
		if err := mm.Validate(*out); err != nil {
			verrs[ModelLevelKey] = err.Error()
		}
	}
	if len(verrs) > 0 {
		return verrs
	}
	return nil
}

// parseCSVImport reads CSV from src and binds each data row onto a T, fetching
// the existing row first when an ID is given (so omitted/absent columns keep
// their current value) and starting from zero otherwise. It validates every
// row but persists NOTHING — the caller decides whether to commit the returned
// plan. rowErrs holds human-readable per-row parse/validation messages; when
// it's non-empty the whole file should be rejected (fail-closed import). fatal
// is a non-row error (bad CSV structure, backend Get failure).
func parseCSVImport[T any](ctx context.Context, mm MetaModel[T], data Accessor[T], src io.Reader) (plan []plannedUpsert[T], rowErrs []string, fatal error) {
	cr := csv.NewReader(src)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // map by header, tolerate ragged rows

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, []string{"empty CSV — no header row"}, nil
	}
	if err != nil {
		return nil, nil, err
	}

	// Map each header cell to a field (by FormFieldName, with Name as a
	// friendly alias), the ID column, or ignore.
	byKey := make(map[string]MetaField, len(mm.Fields)*2)
	for _, mf := range csvImportFields(mm) {
		byKey[csvColKey(mf)] = mf
		byKey[mf.Name] = mf // accept the field name too (e.g. "Owner" for "OwnerID")
	}
	type column struct {
		mf    MetaField
		key   string
		isID  bool
		known bool
	}
	cols := make([]column, len(header))
	for i, h := range header {
		h = strings.TrimSpace(h)
		switch {
		case strings.EqualFold(h, csvIDColumn):
			cols[i] = column{isID: true}
		default:
			mf, ok := byKey[h]
			cols[i] = column{mf: mf, key: csvColKey(mf), known: ok}
		}
	}

	line := 1 // header was line 1; data rows start at 2 (spreadsheet-aligned)
	for {
		rec, rerr := cr.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		line++
		if rerr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: %v", line, rerr))
			continue
		}

		var id uint
		form := map[string][]string{}
		present := map[string]bool{}
		badID := false
		for i := 0; i < len(rec) && i < len(cols); i++ {
			c := cols[i]
			switch {
			case c.isID:
				if s := strings.TrimSpace(rec[i]); s != "" {
					n, perr := strconv.ParseUint(s, 10, 64)
					if perr != nil {
						rowErrs = append(rowErrs, fmt.Sprintf("row %d: invalid ID %q", line, rec[i]))
						badID = true
						break
					}
					id = uint(n)
				}
			case c.known:
				// A NoExport (secret) field with a blank cell is left
				// untouched — never bound to an empty value, so a re-hash to
				// hash("") can't happen.
				if c.mf.NoExport && strings.TrimSpace(rec[i]) == "" {
					continue
				}
				form[c.key] = csvCellToForm(c.mf, rec[i])
				present[c.key] = true
			}
		}
		if badID {
			continue
		}

		// Update starts from the live row so unspecified columns are
		// preserved; create starts from zero.
		var base T
		if id != 0 {
			existing, gerr := data.Get(ctx, id)
			if errors.Is(gerr, ErrNotFound) {
				rowErrs = append(rowErrs, fmt.Sprintf("row %d: ID %d not found", line, id))
				continue
			}
			if gerr != nil {
				return nil, nil, gerr
			}
			base = existing
		}
		if berr := bindCSVRow(mm, form, present, &base); berr != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("row %d: %s", line, berr.Error()))
			continue
		}
		plan = append(plan, plannedUpsert[T]{id: id, row: base})
	}
	return plan, rowErrs, nil
}
