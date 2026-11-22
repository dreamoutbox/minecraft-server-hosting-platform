package files

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestToolbarModalsWebEditAndZipDownload(t *testing.T) {
	r, _, secret, serverDir, otherDir := setupFilesTestRouter(t)

	loginTok, _, _, err := authkit.GenerateLoginToken(1, "owner@example.com", authkit.UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken: %v", err)
	}
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodGet, "/login?token="+url.QueryEscape(loginTok), nil)
	r.ServeHTTP(wLogin, reqLogin)
	cookie := extractSessionCookie(wLogin.Result())
	if cookie == nil {
		t.Fatal("expected session cookie")
	}

	// Create folder + editable file + non-editable file
	if err := os.MkdirAll(filepath.Join(serverDir, "config"), 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "config", "paper.yml"), []byte("verbose: false\n"), 0644); err != nil {
		t.Fatalf("write paper.yml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, ".rcon-cli.yaml"), []byte("host: localhost\n"), 0644); err != nil {
		t.Fatalf("write .rcon-cli.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, ".paper.env"), []byte("EULA=TRUE\n"), 0644); err != nil {
		t.Fatalf("write .paper.env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "binary.dat"), []byte{0x00, 0x1f, 0x8b}, 0644); err != nil {
		t.Fatalf("write binary.dat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "server.jar"), []byte("jar-bytes"), 0644); err != nil {
		t.Fatalf("write server.jar: %v", err)
	}

	// 1. POST /create-file creates eula.txt
	cfForm := url.Values{}
	cfForm.Set("path", serverDir)
	cfForm.Set("name", "eula.txt")
	wCF := httptest.NewRecorder()
	reqCF, _ := http.NewRequest(http.MethodPost, "/create-file", strings.NewReader(cfForm.Encode()))
	reqCF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqCF.AddCookie(cookie)
	r.ServeHTTP(wCF, reqCF)
	if wCF.Code != http.StatusFound {
		t.Fatalf("expected 302 from /create-file, got %d", wCF.Code)
	}
	if _, err := os.Stat(filepath.Join(serverDir, "eula.txt")); err != nil {
		t.Fatalf("expected eula.txt to exist on disk: %v", err)
	}

	// 2. Browse UI checks (no [DIR], blue dir link, toolbar modals, no panels/inline rename input, Edit + Download Zip buttons)
	wBrowse := httptest.NewRecorder()
	reqBrowse, _ := http.NewRequest(http.MethodGet, "/files?path="+url.QueryEscape(serverDir), nil)
	reqBrowse.AddCookie(cookie)
	r.ServeHTTP(wBrowse, reqBrowse)
	if wBrowse.Code != http.StatusOK {
		t.Fatalf("expected 200 on browse, got %d", wBrowse.Code)
	}
	html := wBrowse.Body.String()
	if strings.Contains(html, "[DIR]") {
		t.Errorf("UI must not contain [DIR] prefix")
	}
	if !strings.Contains(html, `class="link-primary fw-semibold text-decoration-none">config</a>`) {
		t.Errorf("expected directory to render as blue link without [DIR]")
	}
	if !strings.Contains(html, `<span class="text-body">eula.txt</span>`) {
		t.Errorf("expected file to render as normal text")
	}
	if strings.Contains(html, `id="uploadFileModal"`) || strings.Contains(html, `data-bs-target="#uploadFileModal"`) {
		t.Errorf("uploadFileModal must be removed in favor of direct file picker")
	}
	if !strings.Contains(html, `id="uploadFileBtn"`) || !strings.Contains(html, `id="directUploadForm"`) || !strings.Contains(html, `id="uploadFileInput"`) {
		t.Errorf("expected direct upload button and hidden form in toolbar")
	}
	if formIdx := strings.Index(html, `id="directUploadForm"`); formIdx >= 0 {
		formSnippet := html[formIdx:]
		pathIdx := strings.Index(formSnippet, `name="path"`)
		fileIdx := strings.Index(formSnippet, `name="file"`)
		if pathIdx < 0 || fileIdx < 0 || pathIdx >= fileIdx {
			t.Errorf("expected path field before file input in directUploadForm")
		}
	}
	if !strings.Contains(html, `data-bs-target="#newFileModal"`) || !strings.Contains(html, `data-bs-target="#newFolderModal"`) {
		t.Errorf("expected sub-navbar toolbar buttons for new file and new folder")
	}
	if strings.Contains(html, `<div class="col-lg-4">`) {
		t.Errorf("right-hand upload/new-folder panels should be removed")
	}
	if !strings.Contains(html, `href="/download-zip?path=`) {
		t.Errorf("expected Download Zip button for directory")
	}
	if !strings.Contains(html, `data-bs-target="#renameModal"`) {
		t.Errorf("expected Rename button to trigger #renameModal")
	}
	if !strings.Contains(html, `data-bs-target="#editFileModal" data-path="`+filepath.Join(serverDir, "eula.txt")) {
		t.Errorf("expected Edit modal trigger for eula.txt")
	}
	if !strings.Contains(html, `data-bs-target="#editFileModal" data-path="`+filepath.Join(serverDir, ".rcon-cli.yaml")) {
		t.Errorf("expected Edit modal trigger for .rcon-cli.yaml")
	}
	if !strings.Contains(html, `data-bs-target="#editFileModal" data-path="`+filepath.Join(serverDir, ".paper.env")) {
		t.Errorf("expected Edit modal trigger for .paper.env")
	}
	if strings.Contains(html, `data-bs-target="#editFileModal" data-path="`+filepath.Join(serverDir, "binary.dat")) {
		t.Errorf("binary.dat must not show an Edit button")
	}
	if strings.Contains(html, `data-bs-target="#editFileModal" data-path="`+filepath.Join(serverDir, "server.jar")) {
		t.Errorf("server.jar must not show an Edit button")
	}

	// Open .rcon-cli.yaml and .paper.env via GET /file-content
	for _, tc := range []struct {
		rel  string
		want string
	}{
		{".rcon-cli.yaml", "host: localhost\n"},
		{".paper.env", "EULA=TRUE\n"},
	} {
		wText := httptest.NewRecorder()
		reqText, _ := http.NewRequest(http.MethodGet, "/file-content?path="+url.QueryEscape(filepath.Join(serverDir, tc.rel)), nil)
		reqText.AddCookie(cookie)
		r.ServeHTTP(wText, reqText)
		if wText.Code != http.StatusOK {
			t.Fatalf("expected 200 on /file-content for %s, got %d (%s)", tc.rel, wText.Code, wText.Body.String())
		}
	}

	// 3. GET /file-content & POST /edit on editable file
	ymlPath := filepath.Join(serverDir, "config", "paper.yml")
	wGet := httptest.NewRecorder()
	reqGet, _ := http.NewRequest(http.MethodGet, "/file-content?path="+url.QueryEscape(ymlPath), nil)
	reqGet.AddCookie(cookie)
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on /file-content, got %d (%s)", wGet.Code, wGet.Body.String())
	}
	var fcPayload struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(wGet.Body.Bytes(), &fcPayload); err != nil {
		t.Fatalf("json unmarshal /file-content: %v", err)
	}
	if fcPayload.Name != "paper.yml" || fcPayload.Content != "verbose: false\n" {
		t.Fatalf("unexpected /file-content payload: %+v", fcPayload)
	}

	// Non-editable .jar rejected by /file-content
	wBadExt := httptest.NewRecorder()
	reqBadExt, _ := http.NewRequest(http.MethodGet, "/file-content?path="+url.QueryEscape(filepath.Join(serverDir, "server.jar")), nil)
	reqBadExt.AddCookie(cookie)
	r.ServeHTTP(wBadExt, reqBadExt)
	if wBadExt.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on /file-content for .jar, got %d", wBadExt.Code)
	}

	// POST /edit saves content
	editForm := url.Values{}
	editForm.Set("path", ymlPath)
	editForm.Set("current_dir", filepath.Join(serverDir, "config"))
	editForm.Set("content", "verbose: true\n")
	wEdit := httptest.NewRecorder()
	reqEdit, _ := http.NewRequest(http.MethodPost, "/edit", strings.NewReader(editForm.Encode()))
	reqEdit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqEdit.AddCookie(cookie)
	r.ServeHTTP(wEdit, reqEdit)
	if wEdit.Code != http.StatusFound {
		t.Fatalf("expected 302 on POST /edit, got %d", wEdit.Code)
	}
	savedBytes, err := os.ReadFile(ymlPath)
	if err != nil || string(savedBytes) != "verbose: true\n" {
		t.Fatalf("unexpected saved content on disk: %q (err=%v)", string(savedBytes), err)
	}

	// 4. GET /download-zip streams folder contents & blocks unauthorized path
	wZipForbidden := httptest.NewRecorder()
	reqZipForbidden, _ := http.NewRequest(http.MethodGet, "/download-zip?path="+url.QueryEscape(otherDir), nil)
	reqZipForbidden.AddCookie(cookie)
	r.ServeHTTP(wZipForbidden, reqZipForbidden)
	if wZipForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on unauthorized /download-zip, got %d", wZipForbidden.Code)
	}

	wZip := httptest.NewRecorder()
	reqZip, _ := http.NewRequest(http.MethodGet, "/download-zip?path="+url.QueryEscape(filepath.Join(serverDir, "config")), nil)
	reqZip.AddCookie(cookie)
	r.ServeHTTP(wZip, reqZip)
	if wZip.Code != http.StatusOK {
		t.Fatalf("expected 200 on /download-zip, got %d (%s)", wZip.Code, wZip.Body.String())
	}
	if !strings.Contains(wZip.Header().Get("Content-Disposition"), "config.zip") {
		t.Errorf("expected Content-Disposition with config.zip, got %s", wZip.Header().Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(bytes.NewReader(wZip.Body.Bytes()), int64(wZip.Body.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader failed: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "paper.yml" {
		t.Fatalf("expected [paper.yml] inside config.zip, got %+v", zr.File)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("open zipped paper.yml: %v", err)
	}
	zippedBody, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(zippedBody) != "verbose: true\n" {
		t.Fatalf("unexpected zipped content: %q", string(zippedBody))
	}
}

func TestStreamingUploadConcurrencyAndOutOfOrderFields(t *testing.T) {
	r, _, secret, serverDir, _ := setupFilesTestRouter(t)
	h := r // setupFilesTestRouter uses 10MB limit

	loginTok, _, _, err := authkit.GenerateLoginToken(1, "owner@example.com", authkit.UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken: %v", err)
	}
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodGet, "/login?token="+url.QueryEscape(loginTok), nil)
	r.ServeHTTP(wLogin, reqLogin)
	cookie := extractSessionCookie(wLogin.Result())
	if cookie == nil {
		t.Fatal("expected session cookie")
	}

	// 1. Out-of-order multipart upload ('file' before 'path') still succeeds
	var oooBuf bytes.Buffer
	mw := multipart.NewWriter(&oooBuf)
	part, _ := mw.CreateFormFile("file", "reverse-order.txt")
	_, _ = part.Write([]byte("reverse-ok"))
	_ = mw.WriteField("path", serverDir)
	_ = mw.Close()

	wOOO := httptest.NewRecorder()
	reqOOO, _ := http.NewRequest(http.MethodPost, "/upload", &oooBuf)
	reqOOO.Header.Set("Content-Type", mw.FormDataContentType())
	reqOOO.AddCookie(cookie)
	h.ServeHTTP(wOOO, reqOOO)
	if wOOO.Code != http.StatusFound || !strings.Contains(wOOO.Header().Get("Location"), "success=") {
		t.Fatalf("expected out-of-order upload success redirect, got code=%d loc=%s", wOOO.Code, wOOO.Header().Get("Location"))
	}
	got, err := os.ReadFile(filepath.Join(serverDir, "reverse-order.txt"))
	if err != nil || string(got) != "reverse-ok" {
		t.Fatalf("unexpected reverse-order.txt contents: %q (err=%v)", string(got), err)
	}

	// 2. Concurrent GET /files completes immediately while a slow streaming upload is in progress
	ts := httptest.NewServer(h)
	defer ts.Close()

	pr, pw := io.Pipe()
	mwStream := multipart.NewWriter(pw)
	uploadDone := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upload", pr)
		req.Header.Set("Content-Type", mwStream.FormDataContentType())
		req.AddCookie(cookie)
		client := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			uploadDone <- 0
			return
		}
		_ = resp.Body.Close()
		uploadDone <- resp.StatusCode
	}()

	// Write path + start file part, then pause before closing the stream
	_ = mwStream.WriteField("path", serverDir)
	streamPart, _ := mwStream.CreateFormFile("file", "slow-stream.txt")
	_, _ = streamPart.Write([]byte("first-chunk-"))

	browseClient := &http.Client{Timeout: 2 * time.Second}
	browseReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/files?path="+url.QueryEscape(serverDir), nil)
	browseReq.AddCookie(cookie)
	browseResp, err := browseClient.Do(browseReq)
	if err != nil {
		t.Fatalf("concurrent browse blocked while slow upload was open: %v", err)
	}
	_ = browseResp.Body.Close()
	if browseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on concurrent browse, got %d", browseResp.StatusCode)
	}

	_, _ = streamPart.Write([]byte("second-chunk"))
	_ = mwStream.Close()
	_ = pw.Close()

	if code := <-uploadDone; code != http.StatusFound {
		t.Fatalf("expected streaming upload to finish with 302, got %d", code)
	}
	streamed, err := os.ReadFile(filepath.Join(serverDir, "slow-stream.txt"))
	if err != nil || string(streamed) != "first-chunk-second-chunk" {
		t.Fatalf("unexpected slow-stream.txt contents: %q (err=%v)", string(streamed), err)
	}
}

func TestMultiSelectDeleteAndZipDownload(t *testing.T) {
	r, _, secret, serverDir, otherDir := setupFilesTestRouter(t)

	loginTok, _, _, err := authkit.GenerateLoginToken(1, "owner@example.com", authkit.UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken: %v", err)
	}
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodGet, "/login?token="+url.QueryEscape(loginTok), nil)
	r.ServeHTTP(wLogin, reqLogin)
	cookie := extractSessionCookie(wLogin.Result())
	if cookie == nil {
		t.Fatal("expected session cookie")
	}

	if err := os.WriteFile(filepath.Join(serverDir, "one.txt"), []byte("111"), 0644); err != nil {
		t.Fatalf("write one.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "two.txt"), []byte("222"), 0644); err != nil {
		t.Fatalf("write two.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(serverDir, "mods"), 0755); err != nil {
		t.Fatalf("mkdir mods: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "mods", "m.txt"), []byte("mod"), 0644); err != nil {
		t.Fatalf("write mods/m.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write otherDir/secret.txt: %v", err)
	}

	// 1. Browse renders checkboxes and bulk action bar
	wBrowse := httptest.NewRecorder()
	reqBrowse, _ := http.NewRequest(http.MethodGet, "/files?path="+url.QueryEscape(serverDir), nil)
	reqBrowse.AddCookie(cookie)
	r.ServeHTTP(wBrowse, reqBrowse)
	html := wBrowse.Body.String()
	for _, want := range []string{`id="selectAllEntries"`, `class="form-check-input entry-checkbox"`, `id="bulkActionBar"`, `id="bulkDownloadZipBtn"`, `id="bulkDeleteBtn"`} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %s in browse HTML", want)
		}
	}

	// 2. Empty selection rejected cleanly
	wEmptyDel := httptest.NewRecorder()
	emptyForm := url.Values{"current_dir": {serverDir}}
	reqEmptyDel, _ := http.NewRequest(http.MethodPost, "/delete-multi", strings.NewReader(emptyForm.Encode()))
	reqEmptyDel.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqEmptyDel.AddCookie(cookie)
	r.ServeHTTP(wEmptyDel, reqEmptyDel)
	if wEmptyDel.Code != http.StatusFound || !strings.Contains(wEmptyDel.Header().Get("Location"), "error=") {
		t.Fatalf("expected error redirect on empty /delete-multi, got code=%d loc=%s", wEmptyDel.Code, wEmptyDel.Header().Get("Location"))
	}

	wEmptyZip := httptest.NewRecorder()
	reqEmptyZip, _ := http.NewRequest(http.MethodPost, "/download-zip-multi", strings.NewReader(""))
	reqEmptyZip.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqEmptyZip.AddCookie(cookie)
	r.ServeHTTP(wEmptyZip, reqEmptyZip)
	if wEmptyZip.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty /download-zip-multi, got %d", wEmptyZip.Code)
	}

	// 3. Unauthorized / traversal path rejected without partial deletes
	badForm := url.Values{}
	badForm.Add("current_dir", serverDir)
	badForm.Add("paths", filepath.Join(serverDir, "one.txt"))
	badForm.Add("paths", filepath.Join(otherDir, "secret.txt"))
	wBadDel := httptest.NewRecorder()
	reqBadDel, _ := http.NewRequest(http.MethodPost, "/delete-multi", strings.NewReader(badForm.Encode()))
	reqBadDel.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqBadDel.AddCookie(cookie)
	r.ServeHTTP(wBadDel, reqBadDel)
	if wBadDel.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on unauthorized /delete-multi, got %d", wBadDel.Code)
	}
	if _, err := os.Stat(filepath.Join(serverDir, "one.txt")); err != nil {
		t.Fatalf("one.txt should not have been deleted when another path was forbidden: %v", err)
	}

	wBadZip := httptest.NewRecorder()
	reqBadZip, _ := http.NewRequest(http.MethodPost, "/download-zip-multi", strings.NewReader(badForm.Encode()))
	reqBadZip.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqBadZip.AddCookie(cookie)
	r.ServeHTTP(wBadZip, reqBadZip)
	if wBadZip.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on unauthorized /download-zip-multi, got %d", wBadZip.Code)
	}

	// 4. Multi-zip downloads selected file and directory
	zipForm := url.Values{}
	zipForm.Add("paths", filepath.Join(serverDir, "one.txt"))
	zipForm.Add("paths", filepath.Join(serverDir, "mods"))
	wZip := httptest.NewRecorder()
	reqZip, _ := http.NewRequest(http.MethodPost, "/download-zip-multi", strings.NewReader(zipForm.Encode()))
	reqZip.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqZip.AddCookie(cookie)
	r.ServeHTTP(wZip, reqZip)
	if wZip.Code != http.StatusOK {
		t.Fatalf("expected 200 on /download-zip-multi, got %d (%s)", wZip.Code, wZip.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(wZip.Body.Bytes()), int64(wZip.Body.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	zipEntries := make(map[string]string)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		zipEntries[f.Name] = string(b)
	}
	if zipEntries["one.txt"] != "111" || zipEntries["mods/m.txt"] != "mod" {
		t.Fatalf("unexpected multi-zip contents: %+v", zipEntries)
	}

	// 5. Multi-delete removes selected entries and leaves unselected intact
	delForm := url.Values{}
	delForm.Set("current_dir", serverDir)
	delForm.Add("paths", filepath.Join(serverDir, "one.txt"))
	delForm.Add("paths", filepath.Join(serverDir, "mods"))
	wDel := httptest.NewRecorder()
	reqDel, _ := http.NewRequest(http.MethodPost, "/delete-multi", strings.NewReader(delForm.Encode()))
	reqDel.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDel.AddCookie(cookie)
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusFound || !strings.Contains(wDel.Header().Get("Location"), "success=") {
		t.Fatalf("expected success redirect on /delete-multi, got code=%d loc=%s", wDel.Code, wDel.Header().Get("Location"))
	}
	if _, err := os.Stat(filepath.Join(serverDir, "one.txt")); !os.IsNotExist(err) {
		t.Errorf("expected one.txt deleted")
	}
	if _, err := os.Stat(filepath.Join(serverDir, "mods")); !os.IsNotExist(err) {
		t.Errorf("expected mods dir deleted")
	}
	if _, err := os.Stat(filepath.Join(serverDir, "two.txt")); err != nil {
		t.Errorf("expected unselected two.txt to remain: %v", err)
	}
}
