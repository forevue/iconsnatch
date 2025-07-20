package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed static
var staticFS embed.FS // Embed the static directory

// loggingMiddleware logs the incoming HTTP request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request processed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}

func main() {
	listenAddr := flag.String("listen-addr", ":8080", "The address to listen on for HTTP requests.")
	publicURL := flag.String("public-url", "http://localhost:8080", "The public base URL for constructing icon URLs.")
	storageDir := flag.String("storage-dir", "icons", "The directory to store icons in.")
	logLevel := flag.String("log-level", "info", "The minimum log level to output (debug, info, warn, error).")
	flag.Parse()

	// Set up a logger.
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		// Can't use slog if it fails to initialize.
		fmt.Fprintf(os.Stderr, "invalid log level %q: %v\n", *logLevel, err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	slog.Info("application started",
		"listen_addr", *listenAddr,
		"public_url", *publicURL,
		"storage_dir", *storageDir,
		"log_level", *logLevel,
	)
	slog.Debug("logger initialized")

	// Create the icon storage directory.
	slog.Info("checking icon storage directory", "path", *storageDir)
	if err := os.MkdirAll(*storageDir, 0755); err != nil {
		slog.Error("failed to create icon storage directory", "error", err)
		os.Exit(1)
	}
	slog.Info("icon storage directory ensured", "path", *storageDir)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	slog.Debug("signal context set up")

	// Set up the HTTP server.
	mux := http.NewServeMux()

	mux.Handle("/api/v1/resolve", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolveHandler(w, r, *publicURL, *storageDir)
	}))

	iconServer := http.FileServer(http.Dir(*storageDir))
	showHandler := http.StripPrefix("/api/v1/show/", iconServer)
	mux.Handle("/api/v1/show/", showHandler)

	// Serve the embedded static files, stripping the "static" prefix.
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		slog.Error("failed to create sub-filesystem for static assets", "error", err)
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(subFS)))

	server := &http.Server{
		Addr:    *listenAddr,
		Handler: loggingMiddleware(mux), // Apply logging middleware here
	}
	slog.Debug("HTTP server configured")

	// Start the server in a goroutine.
	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("server failed to start: %w", err)
		}
		slog.Debug("server goroutine exiting")
		close(serverErrCh)
	}()

	// Wait for a shutdown signal or server error.
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received, starting graceful shutdown")
	case err := <-serverErrCh:
		if err != nil {
			slog.Error("server failed unexpectedly", "error", err)
			cancel() // Trigger shutdown for other components.
		}
	}

	// Create a context with a timeout for the shutdown.
	slog.Info("initiating graceful server shutdown")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Attempt to gracefully shut down the server.
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown failed", "error", err)
	} else {
		slog.Info("server shutdown complete")
	}
	slog.Info("application exiting")
}
