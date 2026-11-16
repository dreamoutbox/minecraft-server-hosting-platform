package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/fileserver/internal/api"
	"github.com/dreamoutbox/fileserver/internal/config"
	"github.com/dreamoutbox/fileserver/internal/files"
	"github.com/dreamoutbox/fileserver/internal/store"
)

const httpShutdownTimeout = 5 * time.Second

// SetupRouter initializes HTTP routes for both the node management API and browser file UI.
//
// Arguments:
//   - cfg: Loaded fileserver runtime configuration.
//   - st: Initialized SQLite store.
//
// Example usage:
//
//	r := SetupRouter(cfg, st)
func SetupRouter(cfg *config.Config, st *store.Store) *gin.Engine {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	apiHandler := api.NewHandler(st, cfg.JWTSecret, cfg.LoginTokenTTL)
	apiHandler.RegisterRoutes(r, cfg.APIKey)

	filesHandler := files.NewHandler(st, cfg.JWTSecret, cfg.MaxUploadBytes, cfg.CookieSecure)
	filesHandler.RegisterRoutes(r)

	return r
}

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("failed loading fileserver config: %v", err)
	}

	db, err := store.OpenSQLite(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("failed opening fileserver sqlite database: %v", err)
	}

	st := store.New(db, cfg.MountsDir)
	r := SetupRouter(cfg, st)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErrCh := make(chan error, 1)
	go func() {
		log.Printf("Fileserver listening on port %s (mounts=%s, db=%s)", cfg.Port, cfg.MountsDir, cfg.SQLitePath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
		close(serveErrCh)
	}()

	select {
	case serveErr := <-serveErrCh:
		if serveErr != nil {
			log.Fatalf("fileserver exited with error: %v", serveErr)
		}
	case <-sigCtx.Done():
		log.Println("Shutdown signal received; stopping fileserver...")
		ctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[WARN] fileserver shutdown error: %v", err)
		}
	}
}
