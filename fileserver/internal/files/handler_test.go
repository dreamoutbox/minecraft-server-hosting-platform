package files

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/fileserver/internal/store"
)

func setupFilesTestRouter(t *testing.T) (*gin.Engine, *store.Store, string, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmp := t.TempDir()
	mountsDir := filepath.Join(tmp, "mounts")
	serverDir := filepath.Join(mountsDir, "1")
	otherDir := filepath.Join(mountsDir, "2")
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		t.Fatalf("mkdir serverDir: %v", err)
	}
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatalf("mkdir otherDir: %v", err)
	}

	db, err := store.OpenSQLite(filepath.Join(tmp, "files.db"))
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	st := store.New(db, mountsDir)

	ctx := context.Background()
	if _, _, err := st.EnsureUser(ctx, 1, "owner@example.com"); err != nil {
		t.Fatalf("EnsureUser failed: %v", err)
	}
	if _, err := st.Allow(ctx, 1, 1); err != nil {
		t.Fatalf("Allow failed: %v", err)
	}

	secret := "files-handler-secret"
	r := gin.New()
	h := NewHandler(st, secret, 10*1024*1024, false)
	h.RegisterRoutes(r)

	return r, st, secret, serverDir, otherDir
}

func extractSessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

func TestLoginTokenFlowAndBrowserFileOperations(t *testing.T) {
	r, _, secret, serverDir, otherDir := setupFilesTestRouter(t)

	// 1. Tampered / expired token rejected
	wBad := httptest.NewRecorder()
	reqBad, _ := http.NewRequest(http.MethodGet, "/login?token=invalid.jwt.token", nil)
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for tampered token, got %d", wBad.Code)
	}

	expiredTok, _, _, err := authkit.GenerateLoginToken(1, "owner@example.com", authkit.UserRoleUser, secret, time.Millisecond)
	if err != nil {
		t.Fatalf("GenerateLoginToken: %v", err)
	}
	time.Sleep(15 * time.Millisecond)
	wExp := httptest.NewRecorder()
	reqExp, _ := http.NewRequest(http.MethodGet, "/login?token="+url.QueryEscape(expiredTok), nil)
	r.ServeHTTP(wExp, reqExp)
	if wExp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", wExp.Code)
	}

	// 2. Valid login token sets session cookie and redirects to target
	loginTok, _, _, err := authkit.GenerateLoginToken(1, "owner@example.com", authkit.UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken: %v", err)
	}
	loginURL := fmt.Sprintf("/login?token=%s&target=%s", url.QueryEscape(loginTok), url.QueryEscape(serverDir))
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodGet, loginURL, nil)
	r.ServeHTTP(wLogin, reqLogin)
	if wLogin.Code != http.StatusFound {
		t.Fatalf("expected 302 from /login, got %d (%s)", wLogin.Code, wLogin.Body.String())
	}
	if loc := wLogin.Header().Get("Location"); loc != "/files?path="+url.QueryEscape(serverDir) {
		t.Fatalf("unexpected redirect location: %s", loc)
	}
	cookie := extractSessionCookie(wLogin.Result())
	if cookie == nil || cookie.Value == "" {
		t.Fatal("expected session cookie to be set")
	}

	// 3. Replaying the same login token is rejected
	wReplay := httptest.NewRecorder()
	reqReplay, _ := http.NewRequest(http.MethodGet, loginURL, nil)
	r.ServeHTTP(wReplay, reqReplay)
	if wReplay.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on replayed login token, got %d", wReplay.Code)
	}

	// 4. Unauthenticated /files returns 401
	wUnauth := httptest.NewRecorder()
	reqUnauth, _ := http.NewRequest(http.MethodGet, "/files?path="+url.QueryEscape(serverDir), nil)
	r.ServeHTTP(wUnauth, reqUnauth)
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session cookie, got %d", wUnauth.Code)
	}

	// 5. Accessing another server directory returns 403
	wForbidden := httptest.NewRecorder()
	reqForbidden, _ := http.NewRequest(http.MethodGet, "/files?path="+url.QueryEscape(otherDir), nil)
	reqForbidden.AddCookie(cookie)
	r.ServeHTTP(wForbidden, reqForbidden)
	if wForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unpermitted directory, got %d", wForbidden.Code)
	}

	// 6. Create directory via POST /mkdir
	mkdirForm := url.Values{}
	mkdirForm.Set("path", serverDir)
	mkdirForm.Set("name", "world")
	wMkdir := httptest.NewRecorder()
	reqMkdir, _ := http.NewRequest(http.MethodPost, "/mkdir", strings.NewReader(mkdirForm.Encode()))
	reqMkdir.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqMkdir.AddCookie(cookie)
	r.ServeHTTP(wMkdir, reqMkdir)
	if wMkdir.Code != http.StatusFound {
		t.Fatalf("expected 302 after mkdir, got %d", wMkdir.Code)
	}

	// 7. Upload file via POST /upload
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("path", serverDir)
	part, _ := mw.CreateFormFile("file", "server.properties")
	_, _ = part.Write([]byte("motd=HelloFileserver\n"))
	_ = mw.Close()

	wUpload := httptest.NewRecorder()
	reqUpload, _ := http.NewRequest(http.MethodPost, "/upload", body)
	reqUpload.Header.Set("Content-Type", mw.FormDataContentType())
	reqUpload.AddCookie(cookie)
	r.ServeHTTP(wUpload, reqUpload)
	if wUpload.Code != http.StatusFound {
		t.Fatalf("expected 302 after upload, got %d", wUpload.Code)
	}

	// 8. Browse shows both world dir and server.properties
	wBrowse := httptest.NewRecorder()
	reqBrowse, _ := http.NewRequest(http.MethodGet, "/files?path="+url.QueryEscape(serverDir), nil)
	reqBrowse.AddCookie(cookie)
	r.ServeHTTP(wBrowse, reqBrowse)
	if wBrowse.Code != http.StatusOK {
		t.Fatalf("expected 200 on browse, got %d", wBrowse.Code)
	}
	browseHTML := wBrowse.Body.String()
	if !strings.Contains(browseHTML, "world") || !strings.Contains(browseHTML, "server.properties") {
		t.Fatalf("expected uploaded file and folder in listing, got: %s", browseHTML)
	}

	// 9. Download file
	uploadedPath := filepath.Join(serverDir, "server.properties")
	wDL := httptest.NewRecorder()
	reqDL, _ := http.NewRequest(http.MethodGet, "/download?path="+url.QueryEscape(uploadedPath), nil)
	reqDL.AddCookie(cookie)
	r.ServeHTTP(wDL, reqDL)
	if wDL.Code != http.StatusOK || wDL.Body.String() != "motd=HelloFileserver\n" {
		t.Fatalf("unexpected download response: code=%d body=%q", wDL.Code, wDL.Body.String())
	}

	// 10. Rename file
	renameForm := url.Values{}
	renameForm.Set("path", uploadedPath)
	renameForm.Set("current_dir", serverDir)
	renameForm.Set("new_name", "server.properties.bak")
	wRen := httptest.NewRecorder()
	reqRen, _ := http.NewRequest(http.MethodPost, "/rename", strings.NewReader(renameForm.Encode()))
	reqRen.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqRen.AddCookie(cookie)
	r.ServeHTTP(wRen, reqRen)
	if wRen.Code != http.StatusFound {
		t.Fatalf("expected 302 after rename, got %d", wRen.Code)
	}
	if _, err := os.Stat(filepath.Join(serverDir, "server.properties.bak")); err != nil {
		t.Fatalf("renamed file missing on disk: %v", err)
	}

	// 11. Delete file
	delForm := url.Values{}
	delForm.Set("path", filepath.Join(serverDir, "server.properties.bak"))
	delForm.Set("current_dir", serverDir)
	wDel := httptest.NewRecorder()
	reqDel, _ := http.NewRequest(http.MethodPost, "/delete", strings.NewReader(delForm.Encode()))
	reqDel.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDel.AddCookie(cookie)
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusFound {
		t.Fatalf("expected 302 after delete, got %d", wDel.Code)
	}
	if _, err := os.Stat(filepath.Join(serverDir, "server.properties.bak")); !os.IsNotExist(err) {
		t.Fatalf("expected file deleted, stat err=%v", err)
	}

	// 12. Logout clears cookie
	wLogout := httptest.NewRecorder()
	reqLogout, _ := http.NewRequest(http.MethodPost, "/logout", nil)
	reqLogout.AddCookie(cookie)
	r.ServeHTTP(wLogout, reqLogout)
	if wLogout.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", wLogout.Code)
	}
	if cleared := extractSessionCookie(wLogout.Result()); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("expected session cookie cleared, got %+v", cleared)
	}
}
