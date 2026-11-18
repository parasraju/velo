package mock

import (
	"context"
	"testing"

	"velo/internal/protocol"
)

func TestDemoProviderLoops(t *testing.T) {
	p := NewDemo("mock-demo")
	if p.Name() != "mock" || p.Model() != "mock-demo" {
		t.Fatalf("Name/Model = %q/%q", p.Name(), p.Model())
	}

	const stepCount = 4
	// Consume the whole script once, then confirm it rewinds rather than
	// erroring on the next call.
	for i := 0; i < stepCount+1; i++ {
		if _, err := p.Complete(context.Background(), protocol.Request{}); err != nil {
			t.Fatalf("Complete() #%d error: %v", i+1, err)
		}
	}
	// A second full pass should keep working.
	for i := 0; i < stepCount; i++ {
		if _, err := p.Complete(context.Background(), protocol.Request{}); err != nil {
			t.Fatalf("second pass #%d error: %v", i+1, err)
		}
	}
}
