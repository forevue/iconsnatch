package main

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	meter                   = otel.Meter("iconsnatch")
	outgoingRequestsCounter metric.Int64Counter
	savedFilesCounter       metric.Int64Counter
	savedFilesSizeHistogram metric.Int64Histogram
)

func init() {
	var err error
	outgoingRequestsCounter, err = meter.Int64Counter(
		"iconsnatch.outgoing_requests_total",
		metric.WithDescription("The total number of outgoing HTTP requests."),
		metric.WithUnit("{requests}"),
	)
	if err != nil {
		slog.Error("failed to create outgoing_requests_total counter", "error", err)
	}

	savedFilesCounter, err = meter.Int64Counter(
		"iconsnatch.files_saved_total",
		metric.WithDescription("The total number of files saved to disk."),
		metric.WithUnit("{files}"),
	)
	if err != nil {
		slog.Error("failed to create files_saved_total counter", "error", err)
	}

	savedFilesSizeHistogram, err = meter.Int64Histogram(
		"iconsnatch.saved_file_size_bytes",
		metric.WithDescription("The size of saved files in bytes."),
		metric.WithUnit("By"),
	)
	if err != nil {
		slog.Error("failed to create saved_file_size_bytes histogram", "error", err)
	}
}

// recordOutgoingRequest increments the outgoing requests counter.
func recordOutgoingRequest(ctx context.Context, attrs ...attribute.KeyValue) {
	if outgoingRequestsCounter != nil {
		outgoingRequestsCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// recordFileSaved increments the saved files counter and records the file size.
func recordFileSaved(ctx context.Context, size int64, attrs ...attribute.KeyValue) {
	if savedFilesCounter != nil {
		savedFilesCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	if savedFilesSizeHistogram != nil {
		savedFilesSizeHistogram.Record(ctx, size, metric.WithAttributes(attrs...))
	}
}
