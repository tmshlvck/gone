package crud

import (
	"github.com/tmshlvck/gone/auth"
	"gorm.io/gorm"
)

// DeriveGormCRUDTable pairs a GORM-backed Accessor (see GORMAccessor /
// gormaccessor.go) with a CRUDTable carrying the default table-level config.
func DeriveGormCRUDTable[T any](mm MetaModel[T], az auth.Authz, db *gorm.DB) CRUDTable[T] {
	return CRUDTable[T]{
		MetaData:      mm,
		Authz:         az,
		Slug:          defaultSlug(mm.Name),
		CreateEnabled: true,
		EditEnabled:   true,
		DeleteEnabled: true,
		ListID:        "table_" + randSuffix(),
		Data:          GORMAccessor(mm, db),
	}
}
