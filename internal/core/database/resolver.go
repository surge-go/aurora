package database

import (
	"regexp"
	"strings"

	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

const (
	lockingRawSQLCallbackName    = "aurora:db_resolver_locking_raw_sql"
	lockingRawSQLRowCallbackName = "aurora:db_resolver_locking_row_sql"
)

var lockingRawSQLPattern = regexp.MustCompile(`(?i)\bfor\s+(?:no\s+key\s+)?(?:update|share|key\s+share)\b|\block\s+in\s+share\s+mode\b`)

// registerResolverCallbacks adds safeguards around dbresolver's raw SQL guesser.
// The upstream callback treats a raw SELECT as a read unless its final bytes are
// exactly "for update", so common forms such as "FOR UPDATE;" can otherwise hit
// a replica. Locking statements are conservatively sent to the write pool.
func registerResolverCallbacks(db *gorm.DB) error {
	// Register after dbresolver so Before("*") places these guards ahead of its
	// own raw/row routing callbacks in GORM's stable callback order.
	if err := db.Callback().Raw().Before("*").Register(lockingRawSQLCallbackName, func(tx *gorm.DB) {
		routeLockingRawSQL(tx)
	}); err != nil {
		return err
	}
	if err := db.Callback().Row().Before("*").Register(lockingRawSQLRowCallbackName, func(tx *gorm.DB) {
		routeLockingRawSQL(tx)
	}); err != nil {
		return err
	}
	return nil
}

func routeLockingRawSQL(db *gorm.DB) {
	if db.Statement == nil || !isLockingRawSQL(db.Statement.SQL.String()) {
		return
	}
	dbresolver.Write.ModifyStatement(db.Statement)
}

func isLockingRawSQL(query string) bool {
	query = strings.TrimSpace(query)
	query = strings.TrimRight(query, "; 	\r\n")
	return lockingRawSQLPattern.MatchString(query)
}
