package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"velo/internal/protocol"
	"velo/internal/provider"
)

type stubProvider struct {
	name  string
	model string
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Model() string { return s.model }

func (s *stubProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func (s *stubProvider) Complete(context.Context, protocol.Request) (*protocol.Response, error) {
	return nil, nil
}

func (s *stubProvider) Stream(context.Context, protocol.Request) (provider.Stream, error) {
	return nil, nil
}

func (s *stubProvider) Close() error { return nil }

func stubFactory(name string) provider.Factory {
	return func(opts provider.Options) (provider.Provider, error) {
		return &stubProvider{name: name, model: opts.Model}, nil
	}
}

func TestRegistryRegisterAndNames(t *testing.T) {
	r := provider.NewRegistry()
	if got := r.Names(); len(got) != 0 {
		t.Fatalf("Names() = %v, want empty", got)
	}

	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := r.Register(name, stubFactory(name)); err != nil {
			t.Fatalf("Register(%q) error: %v", name, err)
		}
	}

	got := r.Names()
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}

	if !r.Has("alpha") {
		t.Fatal("Has(alpha) = false, want true")
	}
	if r.Has("Alpha") {
		t.Fatal("Has(Alpha) = true, want false (exact match)")
	}
	if r.Has("missing") {
		t.Fatal("Has(missing) = true, want false")
	}
}

func TestRegistryRegisterErrors(t *testing.T) {
	r := provider.NewRegistry()

	cases := []struct {
		name    string
		regName string
		factory provider.Factory
	}{
		{"empty name", "", stubFactory("x")},
		{"whitespace name", "   ", stubFactory("x")},
		{"nil factory", "alpha", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Register(tc.regName, tc.factory)
			if err == nil {
				t.Fatal("Register() error = nil, want error")
			}
			var pe *protocol.ProviderError
			if !errors.As(err, &pe) || pe.Kind != protocol.ErrProtocol {
				t.Fatalf("Register() error = %v, want ErrProtocol ProviderError", err)
			}
		})
	}

	if err := r.Register("alpha", stubFactory("alpha")); err != nil {
		t.Fatalf("first Register() error: %v", err)
	}
	err := r.Register("alpha", stubFactory("alpha-2"))
	if err == nil {
		t.Fatal("duplicate Register() error = nil, want error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) || pe.Kind != protocol.ErrProtocol {
		t.Fatalf("duplicate Register() error = %v, want ErrProtocol ProviderError", err)
	}
}

func TestRegistryNew(t *testing.T) {
	r := provider.NewRegistry()
	if err := r.Register("alpha", stubFactory("alpha")); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	p, err := r.New("alpha", provider.Options{Model: "test-model"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "alpha" || p.Model() != "test-model" {
		t.Fatalf("New() provider = %q/%q, want alpha/test-model", p.Name(), p.Model())
	}

	p2, err := r.New("alpha", provider.Options{Model: "test-model"})
	if err != nil {
		t.Fatalf("second New() error: %v", err)
	}
	if p == p2 {
		t.Fatal("New() cached provider instance, want a fresh instance")
	}
}

func TestRegistryNewUnknown(t *testing.T) {
	r := provider.NewRegistry()
	_, err := r.New("missing", provider.Options{})
	if err == nil {
		t.Fatal("New(missing) error = nil, want error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("New(missing) error = %T, want *protocol.ProviderError", err)
	}
	if pe.Kind != protocol.ErrProtocol {
		t.Fatalf("New(missing) kind = %q, want %q", pe.Kind, protocol.ErrProtocol)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("New(missing) error %q does not name provider", err)
	}
}

func TestRegistryNewNilProvider(t *testing.T) {
	r := provider.NewRegistry()
	if err := r.Register("nil", func(provider.Options) (provider.Provider, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	_, err := r.New("nil", provider.Options{})
	if err == nil {
		t.Fatal("New() error = nil, want error")
	}
	var pe *protocol.ProviderError
	if !errors.As(err, &pe) || pe.Kind != protocol.ErrProtocol {
		t.Fatalf("New() error = %v, want ErrProtocol ProviderError", err)
	}
}
