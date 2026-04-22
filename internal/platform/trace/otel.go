package trace

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func Setup(_ context.Context, serviceName string, cfg configpkg.TracingConfig) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.Enabled {
		provider := sdktrace.NewTracerProvider()
		otel.SetTracerProvider(provider)
		return provider.Shutdown, nil
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(loggingExporter{}),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

type loggingExporter struct{}

func (loggingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		slog.Info("trace span",
			slog.String("name", span.Name()),
			slog.String("trace_id", span.SpanContext().TraceID().String()),
			slog.String("span_id", span.SpanContext().SpanID().String()),
		)
	}
	return nil
}

func (loggingExporter) Shutdown(context.Context) error {
	return nil
}
