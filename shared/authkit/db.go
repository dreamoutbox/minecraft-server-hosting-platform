package authkit

import (
	"fmt"

	"gorm.io/gorm"
)

// AutoMigrate applies database schema migrations for core authkit tables.
//
// Arguments:
//   - db: Active GORM database handle.
//
// Example usage:
//
//	if err := authkit.AutoMigrate(db); err != nil {
//	    return err
//	}
func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&User{}); err != nil {
		return fmt.Errorf("failed migrating authkit tables: %w", err)
	}
	return nil
}
