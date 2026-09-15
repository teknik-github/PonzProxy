// Package dnsprovider publishes the TXT records an ACME dns-01 challenge
// needs. dns-01 is the only challenge that can issue wildcard certificates,
// and the only one that works when the proxy is not reachable from the
// internet on port 80 or 443.
package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Provider publishes and withdraws a single TXT record.
//
// Implementations must be safe for concurrent use: a certificate covering
// several domains solves their challenges in parallel.
type Provider interface {
	// Present publishes value as a TXT record at name, which is already
	// the fully qualified "_acme-challenge.example.com" form.
	Present(ctx context.Context, name, value string) error
	// CleanUp removes a record published by Present. It is called even
	// when validation failed, and must tolerate a record that is already
	// gone.
	CleanUp(ctx context.Context, name, value string) error
}

// Factory builds a provider from the credentials stored with a certificate.
// The credentials are opaque JSON so each provider can define its own shape
// without the rest of ponzproxy knowing about it.
type Factory func(credentials json.RawMessage) (Provider, error)

// Descriptor documents a provider for the UI, so the certificate form can ask
// for the right fields instead of hard-coding them in the frontend.
type Descriptor struct {
	Name        string  `json:"name"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Fields      []Field `json:"fields"`
}

// Field is one credential the provider needs.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	// Secret marks a value the UI must mask and never display back.
	Secret bool   `json:"secret"`
	Help   string `json:"help,omitempty"`
}

var (
	registryMu sync.RWMutex
	registry   = map[string]registration{}
)

type registration struct {
	descriptor Descriptor
	factory    Factory
}

// Register makes a provider available by name. It is called from each
// provider's init, so adding a provider means adding one file.
func Register(d Descriptor, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[d.Name] = registration{descriptor: d, factory: f}
}

// New builds the named provider from its credentials.
func New(name string, credentials json.RawMessage) (Provider, error) {
	registryMu.RLock()
	reg, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown dns provider %q", name)
	}
	return reg.factory(credentials)
}

// Descriptors lists every registered provider, for the certificate form.
func Descriptors() []Descriptor {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]Descriptor, 0, len(registry))
	for _, reg := range registry {
		out = append(out, reg.descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
