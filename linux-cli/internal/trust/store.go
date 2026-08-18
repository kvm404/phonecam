// Package trust persists approved phones so a later phonecam start can
// accept them without a new QR. The file lives under XDG_CONFIG_HOME
// (not XDG_RUNTIME_DIR); that directory is the ephemeral session.json.
package trust

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	CurrentVersion = 1
	LaptopIDBytes  = 16
	SecretBytes    = 32
	MaxPhones      = 8
)

var (
	ErrUnknownVersion = errors.New("trusted.json version is not supported")
	ErrNotFound       = errors.New("trusted phone not found")
	ErrEmptyID        = errors.New("phone id is empty")
)

// Phone is one persisted pairing. Secret is the 256-bit pairing_secret.
type Phone struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

// PublicPhone is the GET /trust / CLI list shape. No secrets.
type PublicPhone struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

type fileData struct {
	Version  int     `json:"version"`
	LaptopID string  `json:"laptop_id"`
	Phones   []Phone `json:"phones"`
}

// System abstracts env and filesystem access so tests can fake the store.
type System interface {
	Getenv(name string) string
	UserHomeDir() (string, error)
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadFile(path string) ([]byte, error)
}

// OSSystem implements System against the real operating system.
type OSSystem struct{}

func (OSSystem) Getenv(name string) string { return os.Getenv(name) }

func (OSSystem) UserHomeDir() (string, error) { return os.UserHomeDir() }

func (OSSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (OSSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (OSSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Store is the on-disk trusted-phone list plus a stable laptop_id.
type Store struct {
	sys  System
	warn io.Writer
	mu   sync.Mutex
	data fileData
}

// PathFromEnv is $XDG_CONFIG_HOME/phonecam/trusted.json, else
// $HOME/.config/phonecam/trusted.json. It never uses XDG_RUNTIME_DIR.
func PathFromEnv(getenv func(string) string) string {
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "phonecam", "trusted.json")
	}
	return filepath.Join(getenv("HOME"), ".config", "phonecam", "trusted.json")
}

func (s *Store) path() string {
	if xdg := s.sys.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "phonecam", "trusted.json")
	}
	home, err := s.sys.UserHomeDir()
	if err != nil || home == "" {
		home = s.sys.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "phonecam", "trusted.json")
}

func (s *Store) dir() string {
	return filepath.Dir(s.path())
}

// Open loads trusted.json, or creates laptop_id + empty phones when missing.
// Unknown Version returns ErrUnknownVersion and does not write.
func Open(sys System, warn io.Writer) (*Store, error) {
	if sys == nil {
		sys = OSSystem{}
	}
	if warn == nil {
		warn = io.Discard
	}
	store := &Store{sys: sys, warn: warn}
	data, err := sys.ReadFile(store.path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		laptopID, err := randomBase64URL(LaptopIDBytes)
		if err != nil {
			return nil, err
		}
		store.data = fileData{Version: CurrentVersion, LaptopID: laptopID, Phones: []Phone{}}
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}

	var parsed fileData
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed.Version != CurrentVersion {
		return nil, ErrUnknownVersion
	}
	if parsed.LaptopID == "" {
		id, err := randomBase64URL(LaptopIDBytes)
		if err != nil {
			return nil, err
		}
		parsed.LaptopID = id
		store.data = parsed
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	if parsed.Phones == nil {
		parsed.Phones = []Phone{}
	}
	store.data = parsed
	return store, nil
}

func (s *Store) LaptopID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LaptopID
}

func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Phones)
}

// List returns phones without secrets.
func (s *Store) List() []PublicPhone {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PublicPhone, 0, len(s.data.Phones))
	for _, p := range s.data.Phones {
		out = append(out, PublicPhone{
			ID:        p.ID,
			Name:      p.Name,
			CreatedAt: p.CreatedAt,
			LastSeen:  p.LastSeen,
		})
	}
	return out
}

// LookupBySecret is a constant-time compare of the secret for phoneID.
// The slice is always walked so a miss is not a faster path.
func (s *Store) LookupBySecret(phoneID, secret string) (Phone, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := Phone{}
	matched := 0
	id := []byte(phoneID)
	sec := []byte(secret)
	for _, p := range s.data.Phones {
		idOK := subtle.ConstantTimeCompare([]byte(p.ID), id)
		secOK := subtle.ConstantTimeCompare([]byte(p.Secret), sec)
		if idOK&secOK == 1 {
			found = p
			matched = 1
		}
	}
	return found, matched == 1
}

// Upsert writes a phone. Same id rotates the secret (fresh QR pair),
// updates name and last_seen, and keeps created_at. A new id beyond
// MaxPhones evicts the least-recent last_seen and prints one line.
func (s *Store) Upsert(id, name string, now time.Time) (secret string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return "", ErrEmptyID
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	secret, err = randomBase64URL(SecretBytes)
	if err != nil {
		return "", err
	}

	for i, p := range s.data.Phones {
		if p.ID == id {
			p.Name = name
			p.Secret = secret
			p.LastSeen = now.UTC()
			s.data.Phones[i] = p
			if err := s.saveLocked(); err != nil {
				return "", err
			}
			return secret, nil
		}
	}

	if len(s.data.Phones) >= MaxPhones {
		evict := 0
		for i := 1; i < len(s.data.Phones); i++ {
			if s.data.Phones[i].LastSeen.Before(s.data.Phones[evict].LastSeen) {
				evict = i
			}
		}
		gone := s.data.Phones[evict]
		fmt.Fprintf(s.warn, "Warning: trusted-phone limit (%d) reached; evicted %q\n", MaxPhones, gone.Name)
		s.data.Phones = append(s.data.Phones[:evict], s.data.Phones[evict+1:]...)
	}

	s.data.Phones = append(s.data.Phones, Phone{
		ID:        id,
		Name:      name,
		Secret:    secret,
		CreatedAt: now.UTC(),
		LastSeen:  now.UTC(),
	})
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return secret, nil
}

// Touch updates last_seen for a successful trusted reconnect.
func (s *Store) Touch(phoneID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for i, p := range s.data.Phones {
		if p.ID == phoneID {
			s.data.Phones[i].LastSeen = now.UTC()
			_ = s.saveLocked()
			return
		}
	}
}

// Revoke removes phones whose id or name equals idOrName.
func (s *Store) Revoke(idOrName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.Phones[:0]
	removed := 0
	for _, p := range s.data.Phones {
		if p.ID == idOrName || p.Name == idOrName {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	if removed == 0 {
		return ErrNotFound
	}
	s.data.Phones = kept
	return s.saveLocked()
}

// RevokeAll clears phones and keeps laptop_id.
func (s *Store) RevokeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Phones = []Phone{}
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if s.data.Version != CurrentVersion {
		return ErrUnknownVersion
	}
	if s.data.Phones == nil {
		s.data.Phones = []Phone{}
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := s.sys.MkdirAll(s.dir(), 0o700); err != nil {
		return err
	}
	return s.sys.WriteFile(s.path(), data, 0o600)
}

func randomBase64URL(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
