package files

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/go-authkit"
	"github.com/dreamoutbox/go-authkit/authkitjwt"
	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/dreamoutbox/go-authkit/session"
	"github.com/dreamoutbox/fileserver/internal/store"
)

// SessionCookieName is the HTTP cookie carrying the fileserver opaque session token.
const SessionCookieName = "fs_session"

const (
	maxEditableFileBytes    = MaxEditableFileBytes
	maxUploadPathFieldBytes = 4096
)

//go:embed templates/*
var templateFS embed.FS

// Breadcrumb represents one segment in the current directory navigation trail.
type Breadcrumb struct {
	Label   string
	AbsPath string
}

// Handler serves the browser login endpoint and file manager HTML UI.
type Handler struct {
	store           *store.Store
	authsessions    *session.Manager
	jwtSecret       string
	maxUploadBytes  int64
	dailyQuotaBytes int64
	nowFn           func() time.Time
	tmpl            map[string]*template.Template
}

// NewHandler constructs a file manager HTTP handler.
//
// Arguments:
//   - st: Fileserver SQLite store for permissions, usage tracking, and token replay protection.
//   - authsessions: Session manager handling cookie creation, validation, and logout.
//   - jwtSecret: HMAC secret used to verify single-use login handoff tokens.
//   - maxUploadBytes: Maximum allowed multipart upload body size in bytes.
//   - dailyQuotaBytes: Per-user daily upload/download quota in bytes (0 disables quota enforcement).
//
// Example usage:
//
//	h := files.NewHandler(st, authsessions, cfg.JWTSecret, cfg.MaxUploadBytes, cfg.DailyQuotaBytes)
func NewHandler(st *store.Store, authsessions *session.Manager, jwtSecret string, maxUploadBytes int64, dailyQuotaBytes int64) *Handler {
	h := &Handler{
		store:           st,
		authsessions:    authsessions,
		jwtSecret:       jwtSecret,
		maxUploadBytes:  maxUploadBytes,
		dailyQuotaBytes: dailyQuotaBytes,
		nowFn:           time.Now,
		tmpl:            make(map[string]*template.Template),
	}
	h.loadTemplates()
	return h
}

func (h *Handler) loadTemplates() {
	pages := []string{
		"files.html",
		"error.html",
	}

	for _, page := range pages {
		tmpl := template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+page))
		h.tmpl[page] = tmpl
	}
}

func (h *Handler) renderView(c *gin.Context, status int, page string, data map[string]interface{}) {
	tmpl, exists := h.tmpl[page]
	if !exists {
		c.String(http.StatusInternalServerError, "template %s not found", page)
		return
	}

	if data == nil {
		data = make(map[string]interface{})
	}
	if emailVal, ok := c.Get(authkit.ContextEmailKey); ok {
		data["UserEmail"] = emailVal
	}
	if msg := c.Query("success"); msg != "" {
		data["FlashSuccess"] = msg
	}
	if msg := c.Query("error"); msg != "" {
		data["FlashError"] = msg
	}

	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.ExecuteTemplate(c.Writer, "layout", data)
}

func (h *Handler) renderError(c *gin.Context, status int, title string, message string) {
	h.renderView(c, status, "error.html", map[string]interface{}{
		"Title":   title,
		"Message": message,
	})
}

// LoginWithToken exchanges a single-use login token for a session cookie and redirects to /files.
//
// Arguments:
//   - c: Active Gin request context with query params 'token' and optional 'target'.
//
// Example usage:
//
//	GET /login?token=<JWT>&target=/var/lib/mc-platform/mounts/1
func (h *Handler) LoginWithToken(c *gin.Context) {
	rawToken := strings.TrimSpace(c.Query("token"))
	if rawToken == "" {
		h.renderError(c, http.StatusUnauthorized, "Missing Login Token", "Open File Manager from your server dashboard to obtain a valid access link.")
		return
	}

	claims, err := authkitjwt.ParseAndValidateToken(rawToken, h.jwtSecret, authkitjwt.TokenTypeLogin)
	if err != nil {
		h.renderError(c, http.StatusUnauthorized, "Invalid or Expired Token", "Your file manager login link is invalid or expired. Please click Manage Files again.")
		return
	}

	var expTime = claims.ExpiresAt.Time
	if err := h.store.ConsumeLoginToken(c.Request.Context(), claims.ID, expTime); err != nil {
		if errors.Is(err, store.ErrTokenAlreadyUsed) {
			h.renderError(c, http.StatusUnauthorized, "Token Already Used", "This login link can only be used once. Please reopen File Manager from your dashboard.")
			return
		}
		h.renderError(c, http.StatusInternalServerError, "Login Error", "Failed validating login token.")
		return
	}

	if err := h.authsessions.SetSession(c, claims.UserID); err != nil {
		h.renderError(c, http.StatusInternalServerError, "Session Error", "Failed establishing session.")
		return
	}

	target := strings.TrimSpace(c.Query("target"))
	if target == "" {
		c.Redirect(http.StatusFound, "/files")
		return
	}
	c.Redirect(http.StatusFound, "/files?path="+url.QueryEscape(target))
}

// Logout revokes the active fileserver session and clears the session cookie.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	POST /logout
func (h *Handler) Logout(c *gin.Context) {
	_ = h.authsessions.Logout(c)
	h.renderError(c, http.StatusOK, "Logged Out", "You have been signed out of the file manager. You can close this tab.")
}

// Browse renders either the user's permitted server roots (when path is empty)
// or the contents of a permitted directory.
//
// Arguments:
//   - c: Active Gin request context authenticated via CookieAuthMiddleware.
//
// Example usage:
//
//	GET /files?path=/var/lib/mc-platform/mounts/1
func (h *Handler) Browse(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		h.renderError(c, http.StatusUnauthorized, "Unauthorized", "Please reopen File Manager from your dashboard.")
		return
	}

	quota := h.buildQuotaDisplay(c.Request.Context(), userID)

	requestedPath := strings.TrimSpace(c.Query("path"))
	if requestedPath == "" {
		perms, err := h.store.ListUserPermissions(c.Request.Context(), userID)
		if err != nil {
			h.renderError(c, http.StatusInternalServerError, "Error", err.Error())
			return
		}
		h.renderView(c, http.StatusOK, "files.html", map[string]interface{}{
			"Permissions": perms,
			"Quota":       quota,
		})
		return
	}

	perm, rel, err := h.authorizePath(c, userID, requestedPath)
	if err != nil {
		return
	}

	entries, err := ListDir(perm.DirPath, rel)
	if err != nil {
		h.renderView(c, http.StatusOK, "files.html", map[string]interface{}{
			"Permission":  perm,
			"CurrentPath": filepath.Join(perm.DirPath, rel),
			"ParentPath":  computeParentPath(perm.DirPath, rel),
			"Breadcrumbs": buildBreadcrumbs(perm, rel),
			"FlashError":  err.Error(),
			"Quota":       quota,
		})
		return
	}

	currentPath := perm.DirPath
	if rel != "." {
		currentPath = filepath.Join(perm.DirPath, rel)
	}

	h.renderView(c, http.StatusOK, "files.html", map[string]interface{}{
		"Permission":  perm,
		"CurrentPath": currentPath,
		"ParentPath":  computeParentPath(perm.DirPath, rel),
		"Breadcrumbs": buildBreadcrumbs(perm, rel),
		"Entries":     entries,
		"Quota":       quota,
	})
}

// Download streams a permitted file as an attachment.
//
// Arguments:
//   - c: Active Gin request context with query parameter 'path'.
//
// Example usage:
//
//	GET /download?path=/var/lib/mc-platform/mounts/1/server.properties
func (h *Handler) Download(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	requestedPath := strings.TrimSpace(c.Query("path"))
	perm, rel, err := h.authorizePath(c, userID, requestedPath)
	if err != nil {
		return
	}

	f, info, cleanup, err := OpenDownload(perm.DirPath, rel)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrCannotModifyRoot) || errors.Is(err, ErrIsDirectory) {
			status = http.StatusBadRequest
		} else {
			status = http.StatusNotFound
		}
		c.String(status, err.Error())
		return
	}
	defer cleanup()

	if _, _, ok := h.checkQuota(c, userID, store.TransferDirectionDownload, info.Size()); !ok {
		return
	}

	fileName := filepath.Base(rel)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	cw := &countingResponseWriter{ResponseWriter: c.Writer}
	http.ServeContent(cw, c.Request, fileName, info.ModTime(), f)
	h.recordUsage(c.Request.Context(), userID, store.TransferDirectionDownload, cw.written)
}

// DownloadZip streams a permitted directory and its subdirectories as a zip archive.
//
// Arguments:
//   - c: Active Gin request context with query parameter 'path'.
//
// Example usage:
//
//	GET /download-zip?path=/var/lib/mc-platform/mounts/1/world
func (h *Handler) DownloadZip(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	requestedPath := strings.TrimSpace(c.Query("path"))
	perm, rel, err := h.authorizePath(c, userID, requestedPath)
	if err != nil {
		return
	}

	zipName, writeZip, cleanup, err := OpenDirForZip(perm.DirPath, rel)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, ErrNotDirectory) {
			status = http.StatusBadRequest
		}
		c.String(status, err.Error())
		return
	}
	defer cleanup()

	if _, _, ok := h.checkQuota(c, userID, store.TransferDirectionDownload, 0); !ok {
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", zipName))
	c.Status(http.StatusOK)
	h.streamZipResponse(c, userID, writeZip, requestedPath)
}

// DownloadZipMulti streams multiple selected files and directories as a single zip archive.
//
// Arguments:
//   - c: Active Gin request context with repeated form field 'paths'.
//
// Example usage:
//
//	POST /download-zip-multi
func (h *Handler) DownloadZipMulti(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	paths := filterNonEmptyPaths(c.PostFormArray("paths"))
	if len(paths) == 0 {
		c.String(http.StatusBadRequest, ErrNoPathsSelected.Error())
		return
	}

	targets, err := h.authorizePaths(c, userID, paths)
	if err != nil {
		return
	}

	rootDir := targets[0].perm.DirPath
	rels := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.perm.DirPath != rootDir {
			c.String(http.StatusBadRequest, "selected paths must be within the same directory root")
			return
		}
		rels = append(rels, t.rel)
	}

	zipName, writeZip, cleanup, err := OpenPathsForZip(rootDir, rels)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, ErrCannotModifyRoot) || errors.Is(err, ErrNoPathsSelected) {
			status = http.StatusBadRequest
		}
		c.String(status, err.Error())
		return
	}
	defer cleanup()

	if _, _, ok := h.checkQuota(c, userID, store.TransferDirectionDownload, 0); !ok {
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", zipName))
	c.Status(http.StatusOK)
	h.streamZipResponse(c, userID, writeZip, "selected")
}

func (h *Handler) streamZipResponse(c *gin.Context, userID uint, writeZip func(io.Writer) error, label string) {
	cw := &countingWriter{w: c.Writer}
	if err := writeZip(cw); err != nil {
		log.Printf("[WARN] failed streaming zip for %s: %v", label, err)
	}
	h.recordUsage(c.Request.Context(), userID, store.TransferDirectionDownload, cw.n)
}

// resolveFileOwner looks up the authenticated user's configured Unix UID/GID in SQLite.
// Returns nil when the user does not exist or has no UID/GID configured.
func (h *Handler) resolveFileOwner(ctx context.Context, userID uint) (*FileOwner, error) {
	user, err := h.store.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed loading user ownership settings: %w", err)
	}
	if user.UID == nil && user.GID == nil {
		return nil, nil
	}
	return &FileOwner{UID: user.UID, GID: user.GID}, nil
}

// Upload streams an uploaded multipart file directly into the authorized target directory
// without buffering the entire payload to /tmp first when the 'path' field precedes 'file'.
//
// Arguments:
//   - c: Active Gin request context with form field 'path' and multipart file 'file'.
//
// Example usage:
//
//	POST /upload
func (h *Handler) Upload(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	owner, err := h.resolveFileOwner(c.Request.Context(), userID)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	remaining, limited, ok := h.checkQuota(c, userID, store.TransferDirectionUpload, 0)
	if !ok {
		return
	}
	if limited && c.Request.ContentLength > 0 && c.Request.ContentLength > remaining+multipartOverheadAllowanceBytes {
		h.renderQuotaExceeded(c, store.TransferDirectionUpload)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadBytes)

	mr, err := c.Request.MultipartReader()
	if err != nil {
		redirectWithMessage(c, "", "", "Invalid upload request")
		return
	}

	var (
		dirPath       string
		sawPath       bool
		uploaded      bool
		uploadedBytes int64
		tmpFile       *os.File
		tmpFilePath   string
		tmpFilename   string
	)
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
		}
		if tmpFilePath != "" {
			_ = os.Remove(tmpFilePath)
		}
	}()

	for {
		part, nextErr := mr.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			redirectUploadError(c, dirPath, nextErr)
			return
		}

		switch part.FormName() {
		case "path":
			raw, readErr := io.ReadAll(io.LimitReader(part, maxUploadPathFieldBytes))
			_ = part.Close()
			if readErr != nil {
				redirectUploadError(c, dirPath, readErr)
				return
			}
			dirPath = strings.TrimSpace(string(raw))
			sawPath = true

		case "file":
			filename := part.FileName()
			if filename == "" {
				_ = part.Close()
				continue
			}
			qr := &quotaLimitReader{r: part, limit: remaining, limited: limited}
			if sawPath {
				perm, rel, authErr := h.authorizePath(c, userID, dirPath)
				if authErr != nil {
					_ = part.Close()
					return
				}
				saveErr := SaveUploadedFile(perm.DirPath, rel, filename, qr, owner)
				_ = part.Close()
				if saveErr != nil {
					if errors.Is(saveErr, errQuotaExceeded) {
						h.renderQuotaExceeded(c, store.TransferDirectionUpload)
						return
					}
					redirectUploadError(c, dirPath, saveErr)
					return
				}
				uploaded = true
				uploadedBytes = qr.read
			} else {
				// Fallback if client sent 'file' before 'path' in multipart body.
				tmpFile, err = os.CreateTemp("", "fileserver-upload-*")
				if err != nil {
					_ = part.Close()
					redirectWithMessage(c, dirPath, "", "Failed preparing upload buffer")
					return
				}
				tmpFilePath = tmpFile.Name()
				tmpFilename = filename
				if _, copyErr := io.Copy(tmpFile, qr); copyErr != nil {
					_ = part.Close()
					if errors.Is(copyErr, errQuotaExceeded) {
						h.renderQuotaExceeded(c, store.TransferDirectionUpload)
						return
					}
					redirectUploadError(c, dirPath, copyErr)
					return
				}
				uploadedBytes = qr.read
				_ = part.Close()
			}

		default:
			_ = part.Close()
		}

		if uploaded {
			break
		}
	}

	if !uploaded && tmpFile != nil {
		perm, rel, authErr := h.authorizePath(c, userID, dirPath)
		if authErr != nil {
			return
		}
		if _, seekErr := tmpFile.Seek(0, io.SeekStart); seekErr != nil {
			redirectWithMessage(c, dirPath, "", "Failed reading buffered upload")
			return
		}
		if saveErr := SaveUploadedFile(perm.DirPath, rel, tmpFilename, tmpFile, owner); saveErr != nil {
			redirectUploadError(c, dirPath, saveErr)
			return
		}
		uploaded = true
	}

	if !uploaded {
		redirectWithMessage(c, dirPath, "", "No file selected or upload exceeded limit")
		return
	}

	h.recordUsage(c.Request.Context(), userID, store.TransferDirectionUpload, uploadedBytes)
	redirectWithMessage(c, dirPath, "File uploaded successfully", "")
}

func redirectUploadError(c *gin.Context, dirPath string, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		redirectWithMessage(c, dirPath, "", "No file selected or upload exceeded limit")
		return
	}
	redirectWithMessage(c, dirPath, "", err.Error())
}

// MakeDir creates a new subdirectory under the authorized target directory.
//
// Arguments:
//   - c: Active Gin request context with form fields 'path' and 'name'.
//
// Example usage:
//
//	POST /mkdir
func (h *Handler) MakeDir(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	dirPath := strings.TrimSpace(c.PostForm("path"))
	perm, rel, err := h.authorizePath(c, userID, dirPath)
	if err != nil {
		return
	}

	owner, err := h.resolveFileOwner(c.Request.Context(), userID)
	if err != nil {
		redirectWithMessage(c, dirPath, "", err.Error())
		return
	}

	folderName := c.PostForm("name")
	if err := CreateDir(perm.DirPath, rel, folderName, owner); err != nil {
		redirectWithMessage(c, dirPath, "", err.Error())
		return
	}

	redirectWithMessage(c, dirPath, "Folder created", "")
}

// CreateFile creates a new empty file inside the authorized target directory.
//
// Arguments:
//   - c: Active Gin request context with form fields 'path' and 'name'.
//
// Example usage:
//
//	POST /create-file
func (h *Handler) CreateFile(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	dirPath := strings.TrimSpace(c.PostForm("path"))
	perm, rel, err := h.authorizePath(c, userID, dirPath)
	if err != nil {
		return
	}

	owner, err := h.resolveFileOwner(c.Request.Context(), userID)
	if err != nil {
		redirectWithMessage(c, dirPath, "", err.Error())
		return
	}

	fileName := c.PostForm("name")
	if err := CreateFile(perm.DirPath, rel, fileName, owner); err != nil {
		redirectWithMessage(c, dirPath, "", err.Error())
		return
	}

	redirectWithMessage(c, dirPath, "File created", "")
}

// GetFileContent returns JSON text content for a supported editable file in an authorized directory.
//
// Arguments:
//   - c: Active Gin request context with query parameter 'path'.
//
// Example usage:
//
//	GET /file-content?path=/var/lib/mc-platform/mounts/1/server.properties
func (h *Handler) GetFileContent(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	requestedPath := strings.TrimSpace(c.Query("path"))
	perm, rel, err := h.store.FindPermissionForPath(c.Request.Context(), userID, requestedPath)
	if err != nil {
		if errors.Is(err, store.ErrPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	content, err := ReadEditableFile(perm.DirPath, rel, maxEditableFileBytes)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrCannotModifyRoot), errors.Is(err, ErrIsDirectory), errors.Is(err, ErrNotEditable), errors.Is(err, ErrBinaryFile):
			status = http.StatusBadRequest
		case errors.Is(err, ErrFileTooLarge):
			status = http.StatusRequestEntityTooLarge
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":    filepath.Base(rel),
		"path":    requestedPath,
		"content": content,
	})
}

// EditFile saves updated text content to an existing supported config or text file.
//
// Arguments:
//   - c: Active Gin request context with form fields 'path', 'current_dir', and 'content'.
//
// Example usage:
//
//	POST /edit
func (h *Handler) EditFile(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	targetPath := strings.TrimSpace(c.PostForm("path"))
	currentDir := strings.TrimSpace(c.PostForm("current_dir"))
	content := c.PostForm("content")

	perm, rel, err := h.authorizePath(c, userID, targetPath)
	if err != nil {
		return
	}

	if currentDir == "" {
		currentDir = filepath.Dir(targetPath)
	}

	if err := SaveEditableFile(perm.DirPath, rel, content, maxEditableFileBytes); err != nil {
		redirectWithMessage(c, currentDir, "", err.Error())
		return
	}

	redirectWithMessage(c, currentDir, "File saved", "")
}

// Rename changes the name of an authorized file or directory within its current folder.
//
// Arguments:
//   - c: Active Gin request context with form fields 'path', 'current_dir', and 'new_name'.
//
// Example usage:
//
//	POST /rename
func (h *Handler) Rename(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	targetPath := strings.TrimSpace(c.PostForm("path"))
	currentDir := strings.TrimSpace(c.PostForm("current_dir"))
	newName := c.PostForm("new_name")

	perm, rel, err := h.authorizePath(c, userID, targetPath)
	if err != nil {
		return
	}

	if currentDir == "" {
		currentDir = filepath.Dir(targetPath)
	}

	if err := RenameEntry(perm.DirPath, rel, newName); err != nil {
		redirectWithMessage(c, currentDir, "", err.Error())
		return
	}

	redirectWithMessage(c, currentDir, "Renamed successfully", "")
}

// Delete removes an authorized file or subdirectory.
//
// Arguments:
//   - c: Active Gin request context with form fields 'path' and optional 'current_dir'.
//
// Example usage:
//
//	POST /delete
func (h *Handler) Delete(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	targetPath := strings.TrimSpace(c.PostForm("path"))
	currentDir := strings.TrimSpace(c.PostForm("current_dir"))

	targets, err := h.authorizePaths(c, userID, []string{targetPath})
	if err != nil {
		return
	}

	if currentDir == "" {
		currentDir = filepath.Dir(targetPath)
	}

	if err := deleteAuthorizedTargets(targets); err != nil {
		redirectWithMessage(c, currentDir, "", err.Error())
		return
	}

	redirectWithMessage(c, currentDir, "Deleted successfully", "")
}

// DeleteMulti removes multiple authorized files or subdirectories in one request.
//
// Arguments:
//   - c: Active Gin request context with repeated form field 'paths' and optional 'current_dir'.
//
// Example usage:
//
//	POST /delete-multi
func (h *Handler) DeleteMulti(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.String(http.StatusUnauthorized, "unauthorized")
		return
	}

	currentDir := strings.TrimSpace(c.PostForm("current_dir"))
	paths := filterNonEmptyPaths(c.PostFormArray("paths"))
	if len(paths) == 0 {
		redirectWithMessage(c, currentDir, "", "No items selected")
		return
	}

	targets, err := h.authorizePaths(c, userID, paths)
	if err != nil {
		return
	}

	if currentDir == "" {
		currentDir = filepath.Dir(paths[0])
	}

	if err := deleteAuthorizedTargets(targets); err != nil {
		redirectWithMessage(c, currentDir, "", err.Error())
		return
	}

	redirectWithMessage(c, currentDir, fmt.Sprintf("Deleted %d item(s)", len(targets)), "")
}

// RegisterRoutes binds login, logout, and authenticated file manager endpoints onto r.
//
// Arguments:
//   - r: Gin router engine.
//
// Example usage:
//
//	filesHandler.RegisterRoutes(router)
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/login", h.LoginWithToken)
	r.POST("/logout", h.Logout)

	protected := r.Group("")
	protected.Use(h.authsessions.Middleware(""))
	{
		protected.GET("/", func(c *gin.Context) {
			c.Redirect(http.StatusFound, "/files")
		})
		protected.GET("/files", h.Browse)
		protected.GET("/download", h.Download)
		protected.GET("/download-zip", h.DownloadZip)
		protected.POST("/download-zip-multi", h.DownloadZipMulti)
		protected.GET("/file-content", h.GetFileContent)
		protected.POST("/upload", h.Upload)
		protected.POST("/create-file", h.CreateFile)
		protected.POST("/mkdir", h.MakeDir)
		protected.POST("/edit", h.EditFile)
		protected.POST("/rename", h.Rename)
		protected.POST("/delete", h.Delete)
		protected.POST("/delete-multi", h.DeleteMulti)
	}
}

type authorizedTarget struct {
	perm *store.DirPermission
	rel  string
}

func filterNonEmptyPaths(rawPaths []string) []string {
	out := make([]string, 0, len(rawPaths))
	for _, raw := range rawPaths {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (h *Handler) authorizePaths(c *gin.Context, userID uint, paths []string) ([]authorizedTarget, error) {
	targets := make([]authorizedTarget, 0, len(paths))
	for _, p := range paths {
		perm, rel, err := h.authorizePath(c, userID, p)
		if err != nil {
			return nil, err
		}
		targets = append(targets, authorizedTarget{perm: perm, rel: rel})
	}
	return targets, nil
}

func deleteAuthorizedTargets(targets []authorizedTarget) error {
	for _, t := range targets {
		if err := DeleteEntry(t.perm.DirPath, t.rel); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) authorizePath(c *gin.Context, userID uint, absPath string) (*store.DirPermission, string, error) {
	perm, rel, err := h.store.FindPermissionForPath(c.Request.Context(), userID, absPath)
	if err != nil {
		if errors.Is(err, store.ErrPermissionDenied) {
			h.renderError(c, http.StatusForbidden, "Access Denied", "You do not have permission to access this directory.")
			return nil, "", err
		}
		h.renderError(c, http.StatusInternalServerError, "Permission Check Failed", err.Error())
		return nil, "", err
	}
	return perm, rel, nil
}

func redirectWithMessage(c *gin.Context, dirPath, successMsg, errMsg string) {
	q := url.Values{}
	if dirPath != "" {
		q.Set("path", dirPath)
	}
	if successMsg != "" {
		q.Set("success", successMsg)
	}
	if errMsg != "" {
		q.Set("error", errMsg)
	}
	c.Redirect(http.StatusFound, "/files?"+q.Encode())
}

func computeParentPath(rootDir, rel string) string {
	if rel == "" || rel == "." {
		return ""
	}
	parentRel := filepath.Dir(rel)
	if parentRel == "." {
		return rootDir
	}
	return filepath.Join(rootDir, parentRel)
}

func buildBreadcrumbs(perm *store.DirPermission, rel string) []Breadcrumb {
	crumbs := []Breadcrumb{
		{
			Label:   fmt.Sprintf("Server #%d", perm.ServerID),
			AbsPath: perm.DirPath,
		},
	}
	if rel == "" || rel == "." {
		return crumbs
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	curr := perm.DirPath
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		curr = filepath.Join(curr, part)
		crumbs = append(crumbs, Breadcrumb{
			Label:   part,
			AbsPath: curr,
		})
	}
	return crumbs
}
