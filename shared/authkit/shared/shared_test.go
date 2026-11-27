package shared

import (
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSharedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed opening sqlite test db: %v", err)
	}
	if err := db.AutoMigrate(&User{}, &Session{}); err != nil {
		t.Fatalf("failed automigrate: %v", err)
	}
	return db
}

func TestPasswordHashing(t *testing.T) {
	password := "SecretP@ssword123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("unexpected error hashing password: %v", err)
	}
	if !CheckPassword(hash, password) {
		t.Error("expected password to match hash")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("expected mismatched password to fail verification")
	}
}

func TestGenerateRandomPassword(t *testing.T) {
	p1, err := GenerateRandomPassword(24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p2, err := GenerateRandomPassword(24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1 == "" || p1 == p2 {
		t.Fatalf("expected distinct non-empty passwords, got %q and %q", p1, p2)
	}
	if _, err := GenerateRandomPassword(0); err == nil {
		t.Fatal("expected error for non-positive byte count")
	}
}

func TestRegisterUserAndAuthenticate(t *testing.T) {
	db := setupSharedTestDB(t)

	if _, err := RegisterUser(db, "not-an-email", "Password123!"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
	if _, err := RegisterUser(db, "short@example.com", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}

	user, err := RegisterUser(db, "  Player@Example.com ", "Password123!")
	if err != nil {
		t.Fatalf("RegisterUser failed: %v", err)
	}
	if user.Email != "player@example.com" || user.Role != UserRoleUser {
		t.Fatalf("unexpected registered user: %+v", user)
	}

	if _, err := RegisterUser(db, "player@example.com", "Password123!"); !errors.Is(err, ErrEmailAlreadyExists) {
		t.Fatalf("expected ErrEmailAlreadyExists, got %v", err)
	}

	authUser, err := Authenticate(db, " player@example.com ", "Password123!")
	if err != nil || authUser.ID != user.ID {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if _, err := Authenticate(db, "player@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}
