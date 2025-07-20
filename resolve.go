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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const maxURLBytes = 1 << 16 // 65,536 bytes

// resolveResponse defines the structure for the JSON response.
type resolveResponse struct {
	IconURL string `json:"icon_url"`
	Filled  bool   `json:"filled"`
}

// httpError sends a JSON error response, logs the error, and records the error on the current span.
func httpError(w http.ResponseWriter, r *http.Request, statusCode int, message string, err error) {
	span := trace.SpanFromContext(r.Context())
	if err != nil {
		slog.Error(message, "error", err)
		span.RecordError(err)
	}
	span.SetStatus(codes.Error, message)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// resolveHandler finds, processes, and saves a website's logo.
// It is fully instrumented with OpenTelemetry.
func resolveHandler(w http.ResponseWriter, r *http.Request, tracer trace.Tracer, publicURL string, storageDir string) {
	ctx, span := tracer.Start(r.Context(), "resolveHandler")
	// Propagate the main span's context. httpError will now be able to access it.
	r = r.WithContext(ctx)
	defer span.End()

	// 1. Get and validate the URL.
	targetURL := r.URL.Query().Get("url")
	span.SetAttributes(attribute.String("request.url", targetURL))

	ctx, validationSpan := tracer.Start(ctx, "validate-and-parse-url")
	if targetURL == "" {
		err := errors.New("url query parameter is required")
		validationSpan.RecordError(err)
		httpError(w, r, http.StatusBadRequest, err.Error(), nil)
		validationSpan.End()
		return
	}
	if len(targetURL) > maxURLBytes {
		httpError(w, r, http.StatusBadRequest, "url field must not be greater than 65,536 bytes", nil)
		validationSpan.End()
		return
	}
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		var parseErr error
		parsedURL, parseErr = url.ParseRequestURI("https://" + targetURL)
		if parseErr != nil {
			httpError(w, r, http.StatusBadRequest, "Invalid URL provided", err)
			validationSpan.End()
			return
		}
	}
	validationSpan.SetAttributes(attribute.String("parsed.url", parsedURL.String()))
	span.SetAttributes(attribute.String("parsed.url", parsedURL.String()))
	validationSpan.End()

	// 3. Find the logo for the given URL.
	ctx, findLogoSpan := tracer.Start(ctx, "FindLogoURL")
	// NOTE: The signature for FindLogoURL must be updated in its definition file
	// to accept a context.Context and trace.Tracer for instrumentation.
	resolvedIcon, err := FindLogoURL(ctx, tracer, parsedURL)
	if err != nil {
		findLogoSpan.RecordError(err)
		httpError(w, r, http.StatusInternalServerError, "Could not find logo for the given URL", err)
		findLogoSpan.End()
		return
	}
	defer resolvedIcon.Body.Close()
	findLogoSpan.End()

	// 4. Remove the background from the logo.
	ctx, patchSpan := tracer.Start(ctx, "RemoveLogoBackground")
	patchedIcon, filled, err := RemoveLogoBackground(ctx, tracer, resolvedIcon)
	if err != nil {
		httpError(w, r, http.StatusInternalServerError, "Unexpected error processing the icon", err)
		patchSpan.End()
		return
	}
	defer patchedIcon.Close()
	patchSpan.SetAttributes(attribute.Bool("icon.filled", filled))
	patchSpan.End()

	// 5. Save the patched icon to a file.
	ctx, saveSpan := tracer.Start(ctx, "save-patched-icon")
	hash := sha256.Sum256([]byte(parsedURL.String()))
	filename := hex.EncodeToString(hash[:]) + ".webp"
	saveSpan.SetAttributes(attribute.String("icon.filename", filename))
	filePath := filepath.Join(storageDir, filename)
	outFile, err := os.Create(filePath)
	if err != nil {
		httpError(w, r, http.StatusInternalServerError, "Failed to create icon file", err)
		saveSpan.End()
		return
	}
	defer outFile.Close()
	bytesWritten, err := io.Copy(outFile, patchedIcon)
	if err != nil {
		os.Remove(filePath) // Attempt to clean up the partial file.
		httpError(w, r, http.StatusInternalServerError, "Failed to write icon to file", err)
		saveSpan.End()
		return
	}
	recordFileSaved(r.Context(), bytesWritten, attribute.String("file.path", filePath))
	saveSpan.End()

	// 6. Send the successful JSON response.
	span.SetStatus(codes.Ok, "success")
	response := resolveResponse{
		IconURL: fmt.Sprintf("%s/api/v1/show/%s", publicURL, filename),
		Filled:  filled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Cache-Control", "max-age=604800, immutable")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode response", "error", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to encode response")
	}
}
