package authkit

import (
	"fmt"

	"gorm.io/gorm"
)

// SetupDB applies database schema setup for core authkit tables (users and sessions).
//
// Arguments:
//   - db: Active GORM database handle.
//
// Example usage:
//
//	if err := authkit.SetupDB(db); err != nil {
//	    return err
//	}
func SetupDB(db *gorm.DB) error {
	if err := db.AutoMigrate(&User{}, &Session{}); err != nil {
		return fmt.Errorf("failed setting up authkit tables: %w", err)
	}
	return nil
}
