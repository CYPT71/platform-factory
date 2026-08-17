package mcp

import (
	"context"
	"fmt"
	"sort"
)

// ResourceHandler produces the current contents of one MCP resource on
// demand - resources in this server are always computed fresh from
// repository/process state, never cached, since the whole point of
// exposing them is that a client can rely on them being current.
type ResourceHandler func(ctx context.Context) (text string, mimeType string, err error)

// Resource is one registered MCP resource: its wire descriptor plus the
// handler that produces its contents.
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	Handler     ResourceHandler
}

type resourceRegistry struct {
	resources map[string]Resource
}

func newResourceRegistry() *resourceRegistry {
	return &resourceRegistry{resources: make(map[string]Resource)}
}

// Add registers a resource. It panics on a duplicate URI - a programming
// error in this server's own wiring, not a runtime condition callers can
// trigger.
func (r *resourceRegistry) Add(res Resource) {
	if res.URI == "" {
		panic("mcp: resource registered with an empty URI")
	}
	if _, exists := r.resources[res.URI]; exists {
		panic(fmt.Sprintf("mcp: duplicate resource registration %q", res.URI))
	}
	if res.MimeType == "" {
		res.MimeType = "text/plain"
	}
	r.resources[res.URI] = res
}

func (r *resourceRegistry) get(uri string) (Resource, bool) {
	res, ok := r.resources[uri]
	return res, ok
}

// list returns every registered resource's descriptor, sorted by URI so
// resources/list output is stable across calls.
func (r *resourceRegistry) list() []resourceDescriptor {
	uris := make([]string, 0, len(r.resources))
	for uri := range r.resources {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	descriptors := make([]resourceDescriptor, 0, len(uris))
	for _, uri := range uris {
		res := r.resources[uri]
		descriptors = append(descriptors, resourceDescriptor{
			URI:         res.URI,
			Name:        res.Name,
			Description: res.Description,
			MimeType:    res.MimeType,
		})
	}
	return descriptors
}
