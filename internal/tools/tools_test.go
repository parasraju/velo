package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type fakeTool struct {
	def  Definition
	exec func(ctx context.Context, call Call) (Result, error)
}

func (f *fakeTool) Definition() Definition { return f.def }

func (f *fakeTool) Execute(ctx context.Context, call Call) (Result, error) {
	if f.exec != nil {
		return f.exec(ctx, call)
	}
	return Result{Content: "ok"}, nil
}

func validDefinition(name string) Definition {
	return Definition{
		Name:        name,
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Category:    CategoryRead,
		ReadOnly:    true,
	}
}

func mustRegister(t *testing.T, r *Registry, tool Tool) {
	t.Helper()
	if err := r.Register(tool); err != nil {
		t.Fatalf("register %v: %v", tool.Definition().Name, err)
	}
}

func TestCategoryValid(t *testing.T) {
	for _, category := range []Category{CategoryRead, CategoryWrite, CategoryExec, CategoryNet} {
		if !category.Valid() {
			t.Errorf("category %q should be valid", category)
		}
	}
	for _, category := range []Category{"", "bogus", "READ", "Read"} {
		if category.Valid() {
			t.Errorf("category %q should be invalid", category)
		}
	}
}

func TestCategoryIsReadOnly(t *testing.T) {
	if !CategoryRead.IsReadOnly() {
		t.Error("read category should be read only")
	}
	for _, category := range []Category{CategoryWrite, CategoryExec, CategoryNet} {
		if category.IsReadOnly() {
			t.Errorf("category %q should not be read only", category)
		}
	}
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("registry is nil")
	}
	if r.Len() != 0 {
		t.Fatalf("expected empty registry, got %d", r.Len())
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}
	if got := r.Names(); len(got) != 0 {
		t.Fatalf("expected empty names, got %d", len(got))
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("unexpected tool found")
	}
}

func TestRegistryRegisterNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected error for nil tool")
	}
	var typedNil *fakeTool
	if err := r.Register(typedNil); err == nil {
		t.Fatal("expected error for typed nil tool")
	}
}

func TestRegistryRegisterInvalid(t *testing.T) {
	tests := []struct {
		name string
		def  Definition
	}{
		{name: "empty name", def: Definition{Name: " ", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true}},
		{name: "uppercase", def: Definition{Name: "Read", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true}},
		{name: "dash", def: Definition{Name: "read-file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true}},
		{name: "space", def: Definition{Name: "read file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true}},
		{name: "missing description", def: Definition{Name: "read_file", Description: "  ", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true}},
		{name: "empty schema", def: Definition{Name: "read_file", Description: "d", InputSchema: nil, Category: CategoryRead, ReadOnly: true}},
		{name: "array schema", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`[]`), Category: CategoryRead, ReadOnly: true}},
		{name: "string schema", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`"x"`), Category: CategoryRead, ReadOnly: true}},
		{name: "invalid schema", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`{"a":`), Category: CategoryRead, ReadOnly: true}},
		{name: "invalid category", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: "bogus", ReadOnly: true}},
		{name: "read not readonly", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: false}},
		{name: "write readonly", def: Definition{Name: "write_file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryWrite, ReadOnly: true}},
		{name: "negative timeout", def: Definition{Name: "read_file", Description: "d", InputSchema: json.RawMessage(`{}`), Category: CategoryRead, ReadOnly: true, Timeout: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			if err := r.Register(&fakeTool{def: tc.def}); err == nil {
				t.Fatal("expected registration error")
			} else if !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("expected ErrInvalidArguments, got %v", err)
			}
			if r.Len() != 0 {
				t.Fatal("invalid tool was stored")
			}
		})
	}
}

func TestRegistryTrimsName(t *testing.T) {
	r := NewRegistry()
	def := validDefinition(" padded ")
	mustRegister(t, r, &fakeTool{def: def})
	if _, ok := r.Get("padded"); !ok {
		t.Fatal("trimmed name not registered")
	}
	if defs := r.List(); len(defs) != 1 || defs[0].Name != "padded" {
		t.Fatalf("unexpected definitions: %+v", defs)
	}
}

func TestRegistryDuplicate(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeTool{def: validDefinition("read_file")})
	err := r.Register(&fakeTool{def: validDefinition("read_file")})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("expected one tool, got %d", r.Len())
	}
}

func TestRegistryListSortedAndCopied(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		mustRegister(t, r, &fakeTool{def: validDefinition(name)})
	}
	defs := r.List()
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = def.Name
	}
	want := []string{"alpha", "middle", "zeta"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
	defs[0].Name = "mutated"
	defs[0].InputSchema[0] = 'X'
	again := r.List()
	if again[0].Name != "alpha" {
		t.Fatalf("list was not copied: %q", again[0].Name)
	}
	if again[0].InputSchema[0] != '{' {
		t.Fatalf("schema was not copied: %q", string(again[0].InputSchema))
	}
	if got := fmt.Sprint(r.Names()); got != fmt.Sprint(want) {
		t.Fatalf("expected names %v, got %v", want, got)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeTool{def: validDefinition("shared")})

	const writers = 40
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("tool_%d", i)
			if err := r.Register(&fakeTool{def: validDefinition(name)}); err != nil {
				errs <- err
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := r.Get("shared"); !ok {
				errs <- errors.New("shared tool not found")
			}
			_ = r.List()
			_ = r.Names()
			_ = r.Len()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if r.Len() != writers+1 {
		t.Fatalf("expected %d tools, got %d", writers+1, r.Len())
	}
}
