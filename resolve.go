package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

const maxURLBytes = 1 << 16 // 65,536 bytes

// resolveResponse defines the structure for the JSON response.
type resolveResponse struct {
	IconURL  string `json:"icon_url"`
	IconHash string `json:"icon_hash"`
	Filled   bool   `json:"filled"`
}

// httpError sends a JSON error response, logs the error, and records the error on the current span.
func httpError(w http.ResponseWriter, r *http.Request, statusCode int, message string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// resolveHandler finds, processes, and saves a website's logo.
// It is fully instrumented with OpenTelemetry.
func resolveHandler(w http.ResponseWriter, r *http.Request, publicURL string, storageDir string) {
	slog.Info("resolve request received", "method", r.Method, "path", r.URL.Path)

	// 1. Get and validate the URL.
	targetURL := r.URL.Query().Get("url")

	if targetURL == "" {
		err := errors.New("url query parameter is required")
		httpError(w, r, http.StatusBadRequest, err.Error(), nil)
		slog.Warn("resolve request failed: URL parameter missing")
		return
	}
	if len(targetURL) > maxURLBytes {
		httpError(w, r, http.StatusBadRequest, "url field must not be greater than 65,536 bytes", nil)
		slog.Warn("resolve request failed: URL too long", "url_length", len(targetURL))
		return
	}
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		var parseErr error
		parsedURL, parseErr = url.ParseRequestURI("https://" + targetURL)
		if parseErr != nil {
			httpError(w, r, http.StatusBadRequest, "Invalid URL provided", err)
			slog.Warn("resolve request failed: invalid URL format", "raw_url", targetURL, "error", parseErr)
			return
		}
	}
	slog.Debug("URL successfully parsed", "url", parsedURL.String())

	// 3. Find the logo for the given URL.
	slog.Debug("attempting to find logo URL", "target_url", parsedURL.String())
	// NOTE: The signature for FindLogoURL must be updated in its definition file
	// to accept a context.Context and trace.Tracer for instrumentation.
	resolvedIcon, err := FindLogoURL(r.Context(), parsedURL)
	if err != nil {
		httpError(w, r, http.StatusInternalServerError, "Could not find logo for the given URL", err)
		slog.Error("failed to find logo for URL", "url", parsedURL.String(), "error", err)
		return
	}
	defer resolvedIcon.Body.Close()
	slog.Debug("logo URL found successfully")

	// 4. Remove the background from the logo.
	slog.Debug("attempting to remove background from logo")
	patchedIcon, filled, err := RemoveLogoBackground(r.Context(), resolvedIcon)
	if err != nil {
		httpError(w, r, http.StatusInternalServerError, "Unexpected error processing the icon", err)
		slog.Error("failed to remove background from logo", "error", err)
		return
	}
	defer patchedIcon.Close()
	slog.Debug("background removal complete", "filled", filled)

	// 5. Save the patched icon to a file.
	hash := sha256.Sum256([]byte(parsedURL.String()))
	filename := hex.EncodeToString(hash[:]) + ".webp"
	filePath := filepath.Join(storageDir, filename)
	slog.Debug("attempting to save icon", "filename", filename, "filepath", filePath)

	outFile, err := os.Create(filePath)
	if err != nil {
		httpError(w, r, http.StatusInternalServerError, "Failed to create icon file", err)
		slog.Error("failed to create icon file", "filepath", filePath, "error", err)
		return
	}
	defer outFile.Close()
	_, err = io.Copy(outFile, patchedIcon)
	if err != nil {
		os.Remove(filePath) // Attempt to clean up the partial file.
		httpError(w, r, http.StatusInternalServerError, "Failed to write icon to file", err)
		slog.Error("failed to write icon to file", "filepath", filePath, "error", err)
		return
	}
	slog.Info("icon saved successfully", "filename", filename)

	// 6. Send the successful JSON response.
	response := resolveResponse{
		IconURL:  fmt.Sprintf("%s/api/v1/show/%s", publicURL, filename),
		IconHash: filename,
		Filled:   filled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Cache-Control", "max-age=604800, immutable")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
	slog.Info("resolve request completed successfully", "icon_url", response.IconURL, "icon_hash", response.IconHash, "filled", response.Filled)
}
