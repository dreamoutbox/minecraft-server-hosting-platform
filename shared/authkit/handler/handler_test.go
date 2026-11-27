package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed opening sqlite test db: %v", err)
	}
	if err := db.AutoMigrate(&shared.User{}, &shared.Session{}); err != nil {
		t.Fatalf("failed automigrate: %v", err)
	}
	return db
}

func setupHandlerTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupHandlerTestDB(t)

	jwtSecret := "test-secret-12345"
	r := gin.New()
	NewHandler(db, jwtSecret).RegisterRoutes(r.Group("/api/v1/auth"))
	return r, db, jwtSecret
}

func TestAuthFlowIntegration(t *testing.T) {
	r, _, jwtSecret := setupHandlerTestRouter(t)

	uniqueEmail := fmt.Sprintf("user-%d@example.com", time.Now().UnixNano())
	password := "SecurePassword123!"

	// 1. Register
	regPayload, _ := json.Marshal(RegisterRequest{Email: uniqueEmail, Password: password})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(regPayload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register expected 201, got %d. Body: %s", w.Code, w.Body.String())
	}

	var regResp struct {
		User   UserResponse         `json:"user"`
		Tokens authkitjwt.TokenPair `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("failed decoding register response: %v", err)
	}
	if regResp.User.Email != uniqueEmail || regResp.Tokens.AccessToken == "" || regResp.Tokens.RefreshToken == "" {
		t.Fatalf("unexpected register response: %+v", regResp)
	}

	// 2. Duplicate registration returns 409
	wDup := httptest.NewRecorder()
	reqDup, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(regPayload))
	reqDup.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wDup, reqDup)
	if wDup.Code != http.StatusConflict {
		t.Errorf("expected duplicate register 409, got %d", wDup.Code)
	}

	// 3. Login with wrong password returns 401
	badLoginPayload, _ := json.Marshal(LoginRequest{Email: uniqueEmail, Password: "wrong"})
	wBadLogin := httptest.NewRecorder()
	reqBadLogin, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(badLoginPayload))
	reqBadLogin.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wBadLogin, reqBadLogin)
	if wBadLogin.Code != http.StatusUnauthorized {
		t.Errorf("expected login with bad password 401, got %d", wBadLogin.Code)
	}

	// 4. Login with correct password returns tokens
	loginPayload, _ := json.Marshal(LoginRequest{Email: uniqueEmail, Password: password})
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(loginPayload))
	reqLogin.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wLogin, reqLogin)
	if wLogin.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d. Body: %s", wLogin.Code, wLogin.Body.String())
	}

	var loginResp struct {
		User   UserResponse         `json:"user"`
		Tokens authkitjwt.TokenPair `json:"tokens"`
	}
	_ = json.Unmarshal(wLogin.Body.Bytes(), &loginResp)

	// 5. Protected profile route without token returns 401
	wNoAuth := httptest.NewRecorder()
	reqNoAuth, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	r.ServeHTTP(wNoAuth, reqNoAuth)
	if wNoAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth header, got %d", wNoAuth.Code)
	}

	// 6. Protected profile route with expired token returns 401
	expiredClaims := authkitjwt.CustomClaims{
		UserID:    regResp.User.ID,
		Email:     uniqueEmail,
		Role:      shared.UserRoleUser,
		TokenType: authkitjwt.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	expiredToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, expiredClaims).SignedString([]byte(jwtSecret))
	wExpired := httptest.NewRecorder()
	reqExpired, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	reqExpired.Header.Set("Authorization", "Bearer "+expiredToken)
	r.ServeHTTP(wExpired, reqExpired)
	if wExpired.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with expired token, got %d", wExpired.Code)
	}

	// 7. Protected profile route with valid access token returns 200
	wProfile := httptest.NewRecorder()
	reqProfile, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	reqProfile.Header.Set("Authorization", "Bearer "+loginResp.Tokens.AccessToken)
	r.ServeHTTP(wProfile, reqProfile)
	if wProfile.Code != http.StatusOK {
		t.Fatalf("expected profile 200, got %d. Body: %s", wProfile.Code, wProfile.Body.String())
	}

	// 8. Update profile password
	updatePayload, _ := json.Marshal(UpdateProfileRequest{
		OldPassword: password,
		NewPassword: "BrandNewPassword456!",
	})
	wUpdate := httptest.NewRecorder()
	reqUpdate, _ := http.NewRequest(http.MethodPut, "/api/v1/auth/profile", bytes.NewBuffer(updatePayload))
	reqUpdate.Header.Set("Authorization", "Bearer "+loginResp.Tokens.AccessToken)
	reqUpdate.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wUpdate, reqUpdate)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("expected profile update 200, got %d. Body: %s", wUpdate.Code, wUpdate.Body.String())
	}

	// 9. Refresh token renewal
	refreshPayload, _ := json.Marshal(RefreshRequest{RefreshToken: loginResp.Tokens.RefreshToken})
	wRefresh := httptest.NewRecorder()
	reqRefresh, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBuffer(refreshPayload))
	reqRefresh.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wRefresh, reqRefresh)
	if wRefresh.Code != http.StatusOK {
		t.Fatalf("expected refresh 200, got %d. Body: %s", wRefresh.Code, wRefresh.Body.String())
	}

	// 10. Logout returns 200
	wLogout := httptest.NewRecorder()
	reqLogout, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	r.ServeHTTP(wLogout, reqLogout)
	if wLogout.Code != http.StatusOK {
		t.Errorf("expected logout 200, got %d", wLogout.Code)
	}
}

func TestRegisterDBError(t *testing.T) {
	db := setupHandlerTestDB(t)
	if err := db.Migrator().DropTable(&shared.User{}); err != nil {
		t.Fatalf("failed dropping users table: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewHandler(db, "test-secret").RegisterRoutes(r.Group("/api/v1/auth"))

	payload, _ := json.Marshal(RegisterRequest{Email: "dberr@example.com", Password: "Password123!"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on DB error during Register, got %d (%s)", w.Code, w.Body.String())
	}
}
