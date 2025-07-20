package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// setupOtelSDK initializes the OpenTelemetry SDK with trace and metric providers.
// It returns a shutdown function to be called on application exit.
func setupOtelSDK(ctx context.Context, endpoint string, insecure bool) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and returns the error.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up resource.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceName("iconsnatch-server"),
		),
	)
	if err != nil {
		handleErr(fmt.Errorf("failed to create resource: %w", err))
		return
	}

	// Set up propagator.
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	traceExporterOpts := []otlptracegrpc.Option{}
	if endpoint != "" {
		traceExporterOpts = append(traceExporterOpts, otlptracegrpc.WithEndpoint(endpoint))
	}
	if insecure {
		traceExporterOpts = append(traceExporterOpts, otlptracegrpc.WithInsecure())
	}
	traceExporter, err := otlptracegrpc.New(ctx, traceExporterOpts...)
	if err != nil {
		handleErr(fmt.Errorf("failed to create trace exporter: %w", err))
		return
	}
	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter),
		trace.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, traceProvider.Shutdown)
	otel.SetTracerProvider(traceProvider)

	// Set up meter provider.
	metricExporterOpts := []otlpmetricgrpc.Option{}
	if endpoint != "" {
		metricExporterOpts = append(metricExporterOpts, otlpmetricgrpc.WithEndpoint(endpoint))
	}
	if insecure {
		metricExporterOpts = append(metricExporterOpts, otlpmetricgrpc.WithInsecure())
	}
	metricExporter, err := otlpmetricgrpc.New(ctx, metricExporterOpts...)
	if err != nil {
		handleErr(fmt.Errorf("failed to create metric exporter: %w", err))
		return
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return
}

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
	otelEndpoint := flag.String("otel-exporter-otlp-endpoint", "localhost:4317", "The OTLP exporter endpoint.")
	otelInsecure := flag.Bool("otel-exporter-otlp-insecure", false, "Use an insecure connection to the OTLP exporter.")
	flag.Parse()

	// Set up a logger.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Create the icon storage directory.
	if err := os.MkdirAll(*storageDir, 0755); err != nil {
		slog.Error("failed to create icon storage directory", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Set up OpenTelemetry.
	otelShutdown, err := setupOtelSDK(ctx, *otelEndpoint, *otelInsecure)
	if err != nil {
		slog.Error("failed to set up OpenTelemetry", "error", err)
		os.Exit(1)
	}
	// Handle shutdown gracefully.
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			slog.Error("failed to shutdown OpenTelemetry", "error", err)
		}
	}()

	appTracer := otel.Tracer("iconsnatch")
	mainCtx, mainSpan := appTracer.Start(ctx, "main.run")
	defer mainSpan.End()

	tracer := otel.Tracer("resolve.go")

	// Set up the HTTP server.
	mux := http.NewServeMux()

	resolveHandlerWithTracer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolveHandler(w, r, tracer, *publicURL, *storageDir)
	})
	otelResolveHandler := otelhttp.NewHandler(resolveHandlerWithTracer, "resolve")
	mux.Handle("/api/v1/resolve", loggingMiddleware(otelResolveHandler))

	iconServer := http.FileServer(http.Dir(*storageDir))
	showHandler := http.StripPrefix("/api/v1/show/", iconServer)
	mux.Handle("/api/v1/show/", loggingMiddleware(showHandler))

	fs := http.FileServer(http.Dir("./static"))
	mux.Handle("/", fs)

	server := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
	}

	// Start the server in a goroutine.
	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("server failed to start: %w", err)
		}
		close(serverErrCh)
	}()

	// Wait for a shutdown signal or server error.
	select {
	case <-mainCtx.Done():
		slog.Info("shutdown signal received, starting graceful shutdown")
	case err := <-serverErrCh:
		if err != nil {
			slog.Error("server failed unexpectedly", "error", err)
			mainSpan.RecordError(err)
			mainSpan.SetStatus(codes.Error, err.Error())
			cancel() // Trigger shutdown for other components.
		}
	}

	// Create a context with a timeout for the shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Attempt to gracefully shut down the server.
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown failed", "error", err)
		mainSpan.RecordError(err)
		mainSpan.SetStatus(codes.Error, err.Error())
	} else {
		slog.Info("server shutdown complete")
	}
}
