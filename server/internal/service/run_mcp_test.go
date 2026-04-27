package service

import (
	"reflect"
	"sort"
	"testing"

	"github.com/mindungil/gil/core/mcpregistry"
)

func TestMergeMCPServers_RegistryOnly(t *testing.T) {
	registry := map[string]mcpregistry.Server{
		"fs":     {Type: "stdio", Command: "echo", Args: []string{"hi"}},
		"issues": {Type: "http", URL: "https://x.example.com/mcp"},
	}
	got := mergeMCPServers(nil, registry)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(got), got)
	}
	if got["fs"].Command != "echo" {
		t.Errorf("fs.Command = %q, want echo", got["fs"].Command)
	}
	if got["issues"].URL != "https://x.example.com/mcp" {
		t.Errorf("issues.URL = %q, want https://x.example.com/mcp", got["issues"].URL)
	}
	// Name field is stamped from the map key.
	if got["fs"].Name != "fs" {
		t.Errorf("expected Name to be stamped from key, got %q", got["fs"].Name)
	}
}

func TestMergeMCPServers_SpecOnly(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"override": {Type: "stdio", Command: "spec-cmd"},
	}
	got := mergeMCPServers(spec, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got["override"].Command != "spec-cmd" {
		t.Errorf("Command = %q, want spec-cmd", got["override"].Command)
	}
}

func TestMergeMCPServers_SpecWinsOnCollision(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	registry := map[string]mcpregistry.Server{
		"fs":     {Type: "stdio", Command: "registry-fs"},
		"issues": {Type: "http", URL: "https://x.example.com/mcp"},
	}
	got := mergeMCPServers(spec, registry)
	if got["fs"].Command != "spec-fs" {
		t.Errorf("expected spec to win, got Command = %q", got["fs"].Command)
	}
	if got["issues"].URL == "" {
		t.Errorf("non-colliding registry entry should remain, got: %+v", got["issues"])
	}
}

func TestMergeMCPServers_BothNil(t *testing.T) {
	got := mergeMCPServers(nil, nil)
	if got == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestMergeMCPServers_DoesNotMutateInputs(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	specCopy := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	registry := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "registry-fs"},
	}
	registryCopy := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "registry-fs"},
	}
	_ = mergeMCPServers(spec, registry)
	if !reflect.DeepEqual(spec, specCopy) {
		t.Errorf("spec mutated: %v vs %v", spec, specCopy)
	}
	if !reflect.DeepEqual(registry, registryCopy) {
		t.Errorf("registry mutated: %v vs %v", registry, registryCopy)
	}
}

func TestShadowedRegistryNames_Empty(t *testing.T) {
	if got := shadowedRegistryNames(nil, nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	if got := shadowedRegistryNames(map[string]mcpregistry.Server{"a": {}}, nil); len(got) != 0 {
		t.Errorf("expected empty when registry empty, got %v", got)
	}
	if got := shadowedRegistryNames(nil, map[string]mcpregistry.Server{"a": {}}); len(got) != 0 {
		t.Errorf("expected empty when spec empty, got %v", got)
	}
}

func TestShadowedRegistryNames_SortedIntersection(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"zebra": {}, "alpha": {}, "mango": {}, "fresh-only": {},
	}
	registry := map[string]mcpregistry.Server{
		"alpha": {}, "mango": {}, "zebra": {}, "registry-only": {},
	}
	got := shadowedRegistryNames(spec, registry)
	want := []string{"alpha", "mango", "zebra"}
	if !sort.StringsAreSorted(got) {
		t.Errorf("output not sorted: %v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
