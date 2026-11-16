package authkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed opening sqlite test db: %v", err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatalf("failed automigrate: %v", err)
	}
	return db
}

func setupTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)

	jwtSecret := "test-secret-12345"
	r := gin.New()
	handler := NewHandler(db, jwtSecret)
	handler.RegisterRoutes(r.Group("/api/v1/auth"))
	return r, db, jwtSecret
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

func TestGenerateAndParseLoginToken(t *testing.T) {
	secret := "login-token-secret"
	tok, exp, jti, err := GenerateLoginToken(42, "player@example.com", UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken failed: %v", err)
	}
	if tok == "" || jti == "" || exp.Before(time.Now()) {
		t.Fatalf("invalid login token output: tok=%q jti=%q exp=%v", tok, jti, exp)
	}

	claims, err := ParseAndValidateToken(tok, secret, TokenTypeLogin)
	if err != nil {
		t.Fatalf("ParseAndValidateToken failed: %v", err)
	}
	if claims.UserID != 42 || claims.ID != jti || claims.Email != "player@example.com" {
		t.Errorf("unexpected claims: %+v", claims)
	}

	// Reject login token when parsed as access token
	if _, err := ParseAndValidateToken(tok, secret, TokenTypeAccess); err == nil {
		t.Error("expected login token to fail access token type validation")
	}
}

func TestCookieAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "cookie-secret"
	r := gin.New()
	r.GET("/protected", CookieAuthMiddleware(secret, "session", "/login"), func(c *gin.Context) {
		uid, err := GetUserIDFromContext(c)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("user=%d", uid))
	})
	r.GET("/api-cookie", CookieAuthMiddleware(secret, "session", ""), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Missing cookie redirects when loginPath is configured
	wNoCookie := httptest.NewRecorder()
	reqNoCookie, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(wNoCookie, reqNoCookie)
	if wNoCookie.Code != http.StatusFound || wNoCookie.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %d (%s)", wNoCookie.Code, wNoCookie.Header().Get("Location"))
	}

	// Missing cookie returns 401 when loginPath is empty
	wNoCookieAPI := httptest.NewRecorder()
	reqNoCookieAPI, _ := http.NewRequest(http.MethodGet, "/api-cookie", nil)
	r.ServeHTTP(wNoCookieAPI, reqNoCookieAPI)
	if wNoCookieAPI.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", wNoCookieAPI.Code)
	}

	// Valid cookie succeeds
	pair, err := GenerateTokenPair(7, "cookie@example.com", UserRoleUser, secret)
	if err != nil {
		t.Fatalf("failed generating tokens: %v", err)
	}
	wValid := httptest.NewRecorder()
	reqValid, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	reqValid.AddCookie(&http.Cookie{Name: "session", Value: pair.AccessToken})
	r.ServeHTTP(wValid, reqValid)
	if wValid.Code != http.StatusOK || wValid.Body.String() != "user=7" {
		t.Errorf("expected 200 user=7, got %d (%s)", wValid.Code, wValid.Body.String())
	}
}

func TestAuthFlowIntegration(t *testing.T) {
	r, db, jwtSecret := setupTestRouter(t)

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
		User   UserResponse `json:"user"`
		Tokens TokenPair    `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("failed decoding register response: %v", err)
	}

	if regResp.User.Email != uniqueEmail {
		t.Errorf("expected email %s, got %s", uniqueEmail, regResp.User.Email)
	}
	if regResp.Tokens.AccessToken == "" || regResp.Tokens.RefreshToken == "" {
		t.Fatal("expected access and refresh tokens to be populated")
	}

	// Direct Authenticate helper check
	authUser, err := Authenticate(db, " "+uniqueEmail+" ", password)
	if err != nil || authUser.ID != regResp.User.ID {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if _, err := Authenticate(db, uniqueEmail, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
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
		User   UserResponse `json:"user"`
		Tokens TokenPair    `json:"tokens"`
	}
	_ = json.Unmarshal(wLogin.Body.Bytes(), &loginResp)

	// 5. Protected profile route without token returns 401
	wNoAuth := httptest.NewRecorder()
	reqNoAuth, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	r.ServeHTTP(wNoAuth, reqNoAuth)
	if wNoAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth header, got %d", wNoAuth.Code)
	}

	// 6. Protected profile route with invalid/expired token returns 401
	wBadToken := httptest.NewRecorder()
	reqBadToken, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	reqBadToken.Header.Set("Authorization", "Bearer invalid-token-contents")
	r.ServeHTTP(wBadToken, reqBadToken)
	if wBadToken.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token, got %d", wBadToken.Code)
	}

	// 7. Protected profile route with expired token returns 401
	expiredClaims := CustomClaims{
		UserID:    regResp.User.ID,
		Email:     uniqueEmail,
		Role:      UserRoleUser,
		TokenType: TokenTypeAccess,
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

	// 8. Protected profile route with valid access token returns 200
	wProfile := httptest.NewRecorder()
	reqProfile, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	reqProfile.Header.Set("Authorization", "Bearer "+loginResp.Tokens.AccessToken)
	r.ServeHTTP(wProfile, reqProfile)
	if wProfile.Code != http.StatusOK {
		t.Fatalf("expected profile 200, got %d. Body: %s", wProfile.Code, wProfile.Body.String())
	}

	// 9. Update profile password
	newPassword := "BrandNewPassword456!"
	updatePayload, _ := json.Marshal(UpdateProfileRequest{
		OldPassword: password,
		NewPassword: newPassword,
	})
	wUpdate := httptest.NewRecorder()
	reqUpdate, _ := http.NewRequest(http.MethodPut, "/api/v1/auth/profile", bytes.NewBuffer(updatePayload))
	reqUpdate.Header.Set("Authorization", "Bearer "+loginResp.Tokens.AccessToken)
	reqUpdate.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wUpdate, reqUpdate)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("expected profile update 200, got %d. Body: %s", wUpdate.Code, wUpdate.Body.String())
	}

	// 10. Refresh token renewal
	refreshPayload, _ := json.Marshal(RefreshRequest{
		RefreshToken: loginResp.Tokens.RefreshToken,
	})
	wRefresh := httptest.NewRecorder()
	reqRefresh, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBuffer(refreshPayload))
	reqRefresh.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wRefresh, reqRefresh)
	if wRefresh.Code != http.StatusOK {
		t.Fatalf("expected refresh 200, got %d. Body: %s", wRefresh.Code, wRefresh.Body.String())
	}

	// 11. Logout returns 200
	wLogout := httptest.NewRecorder()
	reqLogout, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	r.ServeHTTP(wLogout, reqLogout)
	if wLogout.Code != http.StatusOK {
		t.Errorf("expected logout 200, got %d", wLogout.Code)
	}
}
