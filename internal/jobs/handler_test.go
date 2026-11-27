package jobs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/gin-gonic/gin"

	jobworkerjobs "github.com/dreamoutbox/minecraft-server-hosting-platform/jobworker/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

func TestHandlerRejectsInvalidJobID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	jwtSecret := "jobs-handler-test-secret"
	NewHandler((*jobworkerjobs.Service)(nil)).RegisterRoutes(r, jwtSecret)

	tokens, err := authkitjwt.GenerateTokenPair(1, "user@example.com", domain.UserRoleUser, jwtSecret)
	if err != nil {
		t.Fatalf("GenerateTokenPair failed: %v", err)
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/jobs/not-a-number"},
		{http.MethodPost, "/api/v1/jobs/0/retry"},
	} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s status = %d, want 400", tc.method, tc.path, w.Code)
		}
	}
}
