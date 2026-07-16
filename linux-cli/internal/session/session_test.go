package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeFS is an in-memory System that records the perms passed to MkdirAll and
// WriteFile so tests can assert the 0700/0600 bits.
type fakeFS struct {
	env  map[string]string
	uid  int
	tmp  string
	data map[string][]byte

	mkdirPath string
	mkdirPerm os.FileMode
	writePath string
	writePerm os.FileMode

	mkdirErr  error
	writeErr  error
	removeErr error
	removed   []string
}

func newFakeFS() *fakeFS {
	return &fakeFS{env: map[string]string{}, uid: 1000, tmp: "/tmp", data: map[string][]byte{}}
}

func (f *fakeFS) Getenv(name string) string { return f.env[name] }
func (f *fakeFS) Getuid() int               { return f.uid }
func (f *fakeFS) TempDir() string           { return f.tmp }

func (f *fakeFS) MkdirAll(path string, perm os.FileMode) error {
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	f.mkdirPath = path
	f.mkdirPerm = perm
	return nil
}

func (f *fakeFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writePath = path
	f.writePerm = perm
	f.data[path] = append([]byte(nil), data...)
	return nil
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	if data, ok := f.data[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeFS) Remove(path string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, path)
	delete(f.data, path)
	return nil
}

func sampleRecord() Record {
	return Record{
		PID:         4242,
		ControlPort: 47470,
		RTPPort:     47471,
		SessionID:   "sess-abc",
		Device:      "/dev/video10",
		StartedAt:   time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
	}
}

func TestWriteThenReadRoundTripsUnderXDG(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	store := NewStore(fs)

	want := sampleRecord()
	if err := store.Write(want); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	wantPath := "/run/user/1000/phonecam/session.json"
	if fs.writePath != wantPath {
		t.Fatalf("expected write path %q, got %q", wantPath, fs.writePath)
	}
	if fs.mkdirPath != "/run/user/1000/phonecam" {
		t.Fatalf("expected mkdir path %q, got %q", "/run/user/1000/phonecam", fs.mkdirPath)
	}
	if fs.mkdirPerm != 0o700 {
		t.Fatalf("expected dir perm 0700, got %o", fs.mkdirPerm)
	}
	if fs.writePerm != 0o600 {
		t.Fatalf("expected file perm 0600, got %o", fs.writePerm)
	}

	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestPathFallsBackToTempDirWhenXDGUnset(t *testing.T) {
	fs := newFakeFS()
	fs.tmp = "/var/tmp"
	fs.uid = 501
	store := NewStore(fs)

	if err := store.Write(sampleRecord()); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	wantPath := filepath.Join("/var/tmp", "phonecam-501", "session.json")
	if fs.writePath != wantPath {
		t.Fatalf("expected fallback write path %q, got %q", wantPath, fs.writePath)
	}
}

func TestReadMissingReturnsErrNoSession(t *testing.T) {
	fs := newFakeFS()
	store := NewStore(fs)

	if _, err := store.Read(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrNoSession, got %v", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	store := NewStore(fs)

	// Removing when nothing exists must not error.
	if err := store.Remove(); err != nil {
		t.Fatalf("expected nil removing missing file, got %v", err)
	}

	// A not-exist error from the OS is swallowed.
	fs.removeErr = os.ErrNotExist
	if err := store.Remove(); err != nil {
		t.Fatalf("expected nil for os.ErrNotExist, got %v", err)
	}

	// Other errors propagate.
	fs.removeErr = errors.New("permission denied")
	if err := store.Remove(); err == nil {
		t.Fatal("expected propagated remove error")
	}
}

func TestWritePropagatesMkdirError(t *testing.T) {
	fs := newFakeFS()
	fs.mkdirErr = errors.New("boom")
	store := NewStore(fs)

	if err := store.Write(sampleRecord()); err == nil {
		t.Fatal("expected mkdir error to propagate")
	}
}

func TestNewStoreNilUsesOSSystem(t *testing.T) {
	if store := NewStore(nil); store.sys == nil {
		t.Fatal("expected non-nil System when passed nil")
	}
}
