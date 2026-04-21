package trace

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"

	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func Setup(_ context.Context, cfg configpkg.TracingConfig) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	provider := trace.NewTracerProvider()
	if !cfg.Enabled {
		otel.SetTracerProvider(provider)
		return provider.Shutdown, nil
	}

	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
