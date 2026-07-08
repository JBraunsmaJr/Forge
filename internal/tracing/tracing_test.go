package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestTracerInitialization(t *testing.T) {
	tp, err := InitTracer("test-service")
	if err != nil {
		t.Fatalf("failed to initialize tracer: %v", err)
	}
	if tp == nil {
		t.Fatal("expected tracer provider to be non-nil")
	}
	defer tp.Shutdown(context.Background())

	if otel.GetTracerProvider() != tp {
		t.Error("expected global tracer provider to be the one we initialized")
	}

	tracer := Tracer()
	if tracer == nil {
		t.Fatal("expected tracer to be non-nil")
	}

	_, span := tracer.Start(context.Background(), "test-span")
	if !span.IsRecording() {
		t.Error("expected span to be recording")
	}
	span.End()
}
