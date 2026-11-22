package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/fileserver/internal/store"
)

const (
	multipartOverheadAllowanceBytes int64 = 8 * 1024
	bytesPerKB                            = 1024
	bytesPerMB                            = 1024 * bytesPerKB
	bytesPerGB                            = 1024 * bytesPerMB
)

var errQuotaExceeded = errors.New("daily transfer quota exceeded")

// QuotaDisplay carries formatted daily upload/download usage for the file manager UI.
type QuotaDisplay struct {
	Enabled        bool
	LimitFormatted string
	UploadUsed     string
	DownloadUsed   string
}

// countingWriter wraps an io.Writer and counts total bytes written.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// countingResponseWriter wraps an http.ResponseWriter to count response body bytes written.
type countingResponseWriter struct {
	http.ResponseWriter
	written int64
}

func (cw *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := cw.ResponseWriter.Write(p)
	cw.written += int64(n)
	return n, err
}

// quotaLimitReader wraps an io.Reader, counts bytes read, and returns errQuotaExceeded
// if the stream attempts to exceed limit bytes (when limited is true).
type quotaLimitReader struct {
	r       io.Reader
	limit   int64
	limited bool
	read    int64
}

func (qr *quotaLimitReader) Read(p []byte) (int, error) {
	if !qr.limited {
		n, err := qr.r.Read(p)
		qr.read += int64(n)
		return n, err
	}

	remaining := qr.limit - qr.read
	if remaining <= 0 {
		var probe [1]byte
		n, err := qr.r.Read(probe[:])
		if n > 0 {
			return 0, errQuotaExceeded
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}

	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := qr.r.Read(p)
	qr.read += int64(n)
	return n, err
}

func (h *Handler) currentUTCDay() string {
	return h.nowFn().UTC().Format(store.UsageDayFormat)
}

func (h *Handler) remainingQuota(ctx context.Context, userID uint, dir store.TransferDirection) (int64, bool, error) {
	if h.dailyQuotaBytes <= 0 {
		return 0, false, nil
	}
	usage, err := h.store.GetUsage(ctx, userID, h.currentUTCDay())
	if err != nil {
		return 0, true, err
	}

	var used int64
	switch dir {
	case store.TransferDirectionUpload:
		used = usage.UploadBytes
	case store.TransferDirectionDownload:
		used = usage.DownloadBytes
	}

	remaining := h.dailyQuotaBytes - used
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true, nil
}

// checkQuota verifies whether userID has enough daily quota for requestedBytes in dir.
// When exceeded or on store error, it writes the HTTP response (429 or 500) and returns ok=false.
func (h *Handler) checkQuota(c *gin.Context, userID uint, dir store.TransferDirection, requestedBytes int64) (int64, bool, bool) {
	remaining, limited, err := h.remainingQuota(c.Request.Context(), userID, dir)
	if err != nil {
		h.renderError(c, http.StatusInternalServerError, "Quota Check Failed", err.Error())
		return 0, true, false
	}
	if !limited {
		return 0, false, true
	}
	if remaining <= 0 || (requestedBytes > 0 && requestedBytes > remaining) {
		h.renderQuotaExceeded(c, dir)
		return remaining, true, false
	}
	return remaining, true, true
}

func (h *Handler) renderQuotaExceeded(c *gin.Context, dir store.TransferDirection) {
	msg := fmt.Sprintf("Daily %s quota of %s reached. Quota resets at 00:00 UTC.", dir, formatBytes(h.dailyQuotaBytes))
	h.renderError(c, http.StatusTooManyRequests, "Daily Quota Exceeded", msg)
}

func (h *Handler) recordUsage(ctx context.Context, userID uint, dir store.TransferDirection, deltaBytes int64) {
	if deltaBytes <= 0 {
		return
	}
	if err := h.store.AddUsage(ctx, userID, h.currentUTCDay(), dir, deltaBytes); err != nil {
		log.Printf("[WARN] failed recording %s usage (%d bytes) for user %d: %v", dir, deltaBytes, userID, err)
	}
}

func (h *Handler) buildQuotaDisplay(ctx context.Context, userID uint) *QuotaDisplay {
	if h.dailyQuotaBytes <= 0 {
		return nil
	}
	usage, err := h.store.GetUsage(ctx, userID, h.currentUTCDay())
	if err != nil {
		log.Printf("[WARN] failed loading quota display for user %d: %v", userID, err)
		return nil
	}
	return &QuotaDisplay{
		Enabled:        true,
		LimitFormatted: formatBytes(h.dailyQuotaBytes),
		UploadUsed:     formatBytes(usage.UploadBytes),
		DownloadUsed:   formatBytes(usage.DownloadBytes),
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= bytesPerGB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(bytesPerGB))
	case b >= bytesPerMB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(bytesPerMB))
	case b >= bytesPerKB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(bytesPerKB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
