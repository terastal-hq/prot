// Package secrets stores credentials in the operating system's keyring (Secret Service on Linux, Keychain on
// macOS) so they are never written to prot's own files.
package secrets

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// service is the keyring service name every prot secret is filed under.
const service = "prot"

var (
	// ErrNotFound is returned when no secret is stored under a key.
	ErrNotFound = errors.New("secret not found")
	// ErrUnavailable is returned when there is no usable keyring (no Secret Service running, or it is locked
	// and the unlock was refused).
	ErrUnavailable = errors.New("no usable OS keyring")
)

// Store keeps secrets by key.
type Store interface {
	Set(key, secret string) error
	Get(key string) (string, error)
	Delete(key string) error
}

// TunnelKey is the keyring key for a tunnel's SSH secret.
func TunnelKey(tunnelID string) string { return "tunnel:" + tunnelID }

// Keyring is a Store backed by the OS keyring.
type Keyring struct{}

func wrap(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNotFound
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

// Set stores or replaces a secret.
func (Keyring) Set(key, secret string) error { return wrap(keyring.Set(service, key, secret)) }

// Get returns a secret, or ErrNotFound.
func (Keyring) Get(key string) (string, error) {
	secret, err := keyring.Get(service, key)
	return secret, wrap(err)
}

// Delete removes a secret. Deleting one that does not exist is not an error.
func (Keyring) Delete(key string) error {
	if err := wrap(keyring.Delete(service, key)); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// Memory is an in-process Store for tests.
type Memory struct {
	mu      sync.Mutex
	secrets map[string]string
	// Err, when set, makes every call fail with it (to simulate a missing or locked keyring).
	Err error
}

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory { return &Memory{secrets: map[string]string{}} }

// Set implements Store.
func (m *Memory) Set(key, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.secrets[key] = secret
	return nil
}

// Get implements Store.
func (m *Memory) Get(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	secret, ok := m.secrets[key]
	if !ok {
		return "", ErrNotFound
	}
	return secret, nil
}

// Delete implements Store.
func (m *Memory) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	delete(m.secrets, key)
	return nil
}

// Len reports how many secrets are stored.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.secrets)
}
