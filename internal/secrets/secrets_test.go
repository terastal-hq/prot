package secrets

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func exerciseStore(t *testing.T, s Store, key string) {
	t.Helper()
	if _, err := s.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set: %v, want ErrNotFound", err)
	}
	if err := s.Set(key, "first"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(key, "p@ss w0rd/ünïcode\n"); err != nil { // replace, with awkward characters
		t.Fatalf("Set (replace): %v", err)
	}
	if got, err := s.Get(key); err != nil || got != "p@ss w0rd/ünïcode\n" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(key); err != nil {
		t.Errorf("deleting a missing secret must not be an error, got %v", err)
	}
	if _, err := s.Get(key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	m := NewMemory()
	exerciseStore(t, m, TunnelKey("t1"))
	if m.Len() != 0 {
		t.Errorf("Len = %d", m.Len())
	}

	m.Err = ErrUnavailable
	if err := m.Set("k", "v"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("simulated outage: %v", err)
	}
}

func TestTunnelKeyIsNamespaced(t *testing.T) {
	if TunnelKey("abc") != "tunnel:abc" || TunnelKey("a") == TunnelKey("b") {
		t.Error("TunnelKey must be stable and unique per tunnel")
	}
}

// Runs against the real OS keyring, so it is skipped where none is running (CI containers, headless boxes).
func TestRealKeyring(t *testing.T) {
	k := Keyring{}
	probe := fmt.Sprintf("test-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := k.Set(probe, "x"); err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("no OS keyring here: %v", err)
		}
		t.Fatal(err)
	}
	_ = k.Delete(probe)
	exerciseStore(t, k, probe)
}
