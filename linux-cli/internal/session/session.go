// Package session persists a small record describing the running `phonecam
// start` process so that `phonecam status` and `phonecam stop`, invoked from a
// different terminal, can locate the process and its loopback control server.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record captures everything status/stop need to find and verify the running
// start process. It is written as JSON to a per-user runtime file.
type Record struct {
	PID         int       `json:"pid"`
	ControlPort int       `json:"control_port"`
	RTPPort     int       `json:"rtp_port"`
	SessionID   string    `json:"session"`
	Device      string    `json:"device"`
	StartedAt   time.Time `json:"started_at"`
}

// ErrNoSession is returned by Read when no session file exists, i.e. PhoneCam
// is not running (or never recorded a session).
var ErrNoSession = errors.New("no session file")

// System abstracts the OS-level env and filesystem access the Store needs so it
// can be faked in tests. A nil System defaults to OSSystem{}.
type System interface {
	Getenv(name string) string
	Getuid() int
	TempDir() string
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadFile(path string) ([]byte, error)
	Remove(path string) error
}

// OSSystem implements System against the real operating system.
type OSSystem struct{}

func (OSSystem) Getenv(name string) string { return os.Getenv(name) }

func (OSSystem) Getuid() int { return os.Getuid() }

func (OSSystem) TempDir() string { return os.TempDir() }

func (OSSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (OSSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (OSSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (OSSystem) Remove(path string) error { return os.Remove(path) }

// Store reads and writes the session record at a stable per-user path.
type Store struct {
	sys System
}

// NewStore returns a Store backed by sys. A nil sys uses the real OS.
func NewStore(sys System) *Store {
	if sys == nil {
		sys = OSSystem{}
	}
	return &Store{sys: sys}
}

// dir returns the directory that holds the session file. It prefers
// $XDG_RUNTIME_DIR/phonecam and falls back to a per-uid directory in the
// system temp dir when XDG_RUNTIME_DIR is unset.
func (s *Store) dir() string {
	if runtimeDir := s.sys.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "phonecam")
	}
	return filepath.Join(s.sys.TempDir(), fmt.Sprintf("phonecam-%d", s.sys.Getuid()))
}

func (s *Store) path() string {
	return filepath.Join(s.dir(), "session.json")
}

// Write persists record, creating the parent directory (0700) and file (0600)
// if needed.
func (s *Store) Write(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := s.sys.MkdirAll(s.dir(), 0o700); err != nil {
		return err
	}
	return s.sys.WriteFile(s.path(), data, 0o600)
}

// Read loads the persisted record. It returns ErrNoSession when no session file
// exists.
func (s *Store) Read() (Record, error) {
	data, err := s.sys.ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, ErrNoSession
		}
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Remove deletes the session file. It is idempotent: a missing file is not an
// error.
func (s *Store) Remove() error {
	if err := s.sys.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
