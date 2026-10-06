// Package connections defines provider-neutral account-connection contracts.
package connections

import (
	"context"
	"sort"
)

type Provider struct {
	ID           string
	Name         string
	Instructions string
	Mode         string
}

type Registry struct{ providers map[string]Provider }

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, provider := range providers {
		r.providers[provider.ID] = provider
	}
	return r
}

func (r *Registry) Get(id string) (Provider, bool) {
	provider, ok := r.providers[id]
	return provider, ok
}

func (r *Registry) List() []Provider {
	providers := make([]Provider, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers
}

var DefaultRegistry = NewRegistry(
	Provider{ID: "goodreads", Name: "Goodreads Import", Instructions: "Upload a Goodreads Library Export CSV.", Mode: "upload"},
	Provider{ID: "hardcover", Name: "Hardcover Sync", Instructions: "Paste your personal Hardcover API token.", Mode: "token"},
)

type Source interface {
	Snapshot(context.Context, string) (Snapshot, error)
}

type Snapshot struct {
	Shelves []Shelf
	Items   []Item
}

type Shelf struct{ Key, Name string }

type Item struct {
	ShelfKey, ExternalID, Title, Author, ISBN string
	AddedAt                                   int64
	Position                                  int
}
