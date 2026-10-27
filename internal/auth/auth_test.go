package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

func getAuthTestDB(t *testing.T) *domain.User {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil
	}
	if err := domain.AutoMigrate(db); err != nil {
		t.Fatalf("failed automigrate: %v", err)
	}
	return nil
}

func setupTestRouter() (*gin.Engine, string) {
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		panic(err)
	}
	_ = domain.AutoMigrate(db)

	jwtSecret := "test-secret-12345"
	r := gin.New()
	handler := NewHandler(db, jwtSecret)
	handler.RegisterRoutes(r.Group("/api/v1/auth"))
	return r, jwtSecret
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

func TestAuthFlowIntegration(t *testing.T) {
	_ = getAuthTestDB(t)
	r, jwtSecret := setupTestRouter()

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
		Role:      domain.UserRoleUser,
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
