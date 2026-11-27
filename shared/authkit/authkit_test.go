package authkit

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSetupDB(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed opening sqlite test db: %v", err)
	}
	if err := SetupDB(db); err != nil {
		t.Fatalf("SetupDB failed: %v", err)
	}
	if !db.Migrator().HasTable(&User{}) || !db.Migrator().HasTable(&Session{}) {
		t.Fatal("expected SetupDB to create users and sessions tables")
	}
	if db.Migrator().HasColumn(&User{}, "customer_id") || db.Migrator().HasColumn(&User{}, "payment_method_id") {
		t.Fatal("expected authkit User table not to contain billing columns")
	}
}
