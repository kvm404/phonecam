package trust

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	env  map[string]string
	home string
	data map[string][]byte

	mkdirPath string
	mkdirPerm os.FileMode
	writePath string
	writePerm os.FileMode

	mkdirErr error
	writeErr error
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		env:  map[string]string{},
		home: "/home/alice",
		data: map[string][]byte{},
	}
}

func (f *fakeFS) Getenv(name string) string { return f.env[name] }

func (f *fakeFS) UserHomeDir() (string, error) { return f.home, nil }

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

func TestOpenCreatesLaptopIDAndEmptyPhones(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/home/alice/.config"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wantPath := "/home/alice/.config/phonecam/trusted.json"
	if fs.writePath != wantPath {
		t.Fatalf("write path %q, want %q", fs.writePath, wantPath)
	}
	if fs.mkdirPath != "/home/alice/.config/phonecam" {
		t.Fatalf("mkdir path %q", fs.mkdirPath)
	}
	if fs.mkdirPerm != 0o700 {
		t.Fatalf("dir perm %o, want 0700", fs.mkdirPerm)
	}
	if fs.writePerm != 0o600 {
		t.Fatalf("file perm %o, want 0600", fs.writePerm)
	}
	if store.Count() != 0 {
		t.Fatalf("expected empty phones, got %d", store.Count())
	}
	raw, err := base64.RawURLEncoding.DecodeString(store.LaptopID())
	if err != nil || len(raw) != LaptopIDBytes {
		t.Fatalf("laptop_id %q (%v)", store.LaptopID(), err)
	}
	if _, ok := fs.data[wantPath]; !ok {
		t.Fatal("expected trusted.json to be created")
	}
}

func TestPathFallsBackToHomeConfig(t *testing.T) {
	fs := newFakeFS()
	fs.home = "/home/bob"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := "/home/bob/.config/phonecam/trusted.json"
	if fs.writePath != want {
		t.Fatalf("path %q, want %q", fs.writePath, want)
	}
	if store.LaptopID() == "" {
		t.Fatal("expected laptop_id")
	}
}

func TestPathIgnoresXDGRuntimeDir(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	fs.home = "/home/alice"
	_, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if strings.Contains(fs.writePath, "/run/user") {
		t.Fatalf("must not use XDG_RUNTIME_DIR, wrote %q", fs.writePath)
	}
	if fs.writePath != "/home/alice/.config/phonecam/trusted.json" {
		t.Fatalf("unexpected path %q", fs.writePath)
	}
}

func TestPathFromEnvPrefersXDGConfig(t *testing.T) {
	got := PathFromEnv(func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return "/xdg"
		}
		if name == "HOME" {
			return "/home/alice"
		}
		if name == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	})
	if got != filepath.Join("/xdg", "phonecam", "trusted.json") {
		t.Fatalf("got %q", got)
	}
}

func TestOpenUnknownVersionRefusesWrite(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	path := "/cfg/phonecam/trusted.json"
	original := []byte(`{"version":2,"laptop_id":"abc","phones":[]}`)
	fs.data[path] = original
	_, err := Open(fs, nil)
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("expected ErrUnknownVersion, got %v", err)
	}
	if !bytes.Equal(fs.data[path], original) {
		t.Fatalf("unknown version must not rewrite the file: %s", fs.data[path])
	}
}

func TestLookupBySecretConstantTimeMatch(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	secret, err := store.Upsert("phone-1", "Pixel", now)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok := store.LookupBySecret("phone-1", secret)
	if !ok || got.Name != "Pixel" {
		t.Fatalf("expected match, got %#v ok=%v", got, ok)
	}
	if _, ok := store.LookupBySecret("phone-1", "wrong-secret-value-not-the-same"); ok {
		t.Fatal("wrong secret must not match")
	}
	if _, ok := store.LookupBySecret("phone-2", secret); ok {
		t.Fatal("wrong id must not match")
	}
	if _, ok := store.LookupBySecret("phone-1", ""); ok {
		t.Fatal("empty secret must not match")
	}
}

func TestUpsertRotatesSecretOnSameID(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	first, err := store.Upsert("phone-1", "Old", now)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	later := now.Add(time.Hour)
	second, err := store.Upsert("phone-1", "New", later)
	if err != nil {
		t.Fatalf("Upsert rotate: %v", err)
	}
	if first == second {
		t.Fatal("fresh QR pair must rotate the secret")
	}
	if store.Count() != 1 {
		t.Fatalf("same id must not add a second entry, got %d", store.Count())
	}
	got, ok := store.LookupBySecret("phone-1", second)
	if !ok || got.Name != "New" {
		t.Fatalf("rotated secret should match new name, got %#v", got)
	}
	if _, ok := store.LookupBySecret("phone-1", first); ok {
		t.Fatal("old secret must not match after rotate")
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("created_at should stick, got %s", got.CreatedAt)
	}
	if !got.LastSeen.Equal(later) {
		t.Fatalf("last_seen should update, got %s", got.LastSeen)
	}
}

func TestCap8EvictsLeastRecentLastSeen(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	var warn bytes.Buffer
	store, err := Open(fs, &warn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxPhones; i++ {
		id := string(rune('a' + i))
		if _, err := store.Upsert(id, "phone-"+id, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	if _, err := store.Upsert("new", "Ninth", base.Add(24*time.Hour)); err != nil {
		t.Fatalf("9th Upsert: %v", err)
	}
	if store.Count() != MaxPhones {
		t.Fatalf("expected cap %d, got %d", MaxPhones, store.Count())
	}
	if _, ok := store.LookupBySecret("a", "unused"); ok {
		t.Fatal("least-recent last_seen should have been evicted")
	}
	// Confirm "a" is gone via list, not only a dummy secret lookup.
	for _, p := range store.List() {
		if p.ID == "a" {
			t.Fatal("evicted phone still listed")
		}
	}
	if !strings.Contains(warn.String(), "evicted") || !strings.Contains(warn.String(), "phone-a") {
		t.Fatalf("expected one-line eviction warning, got %q", warn.String())
	}
	if strings.Count(warn.String(), "\n") != 1 {
		t.Fatalf("expected one warning line, got %q", warn.String())
	}
}

func TestListOmitsSecrets(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	list := store.List()
	if len(list) != 1 || list[0].ID != "phone-1" || list[0].Name != "Pixel" {
		t.Fatalf("list %#v", list)
	}
	raw := fs.data[fs.writePath]
	var dumped map[string]any
	if err := json.Unmarshal(raw, &dumped); err != nil {
		t.Fatalf("file json: %v", err)
	}
	phones, _ := dumped["phones"].([]any)
	if len(phones) != 1 {
		t.Fatalf("file phones %#v", dumped)
	}
	entry := phones[0].(map[string]any)
	if entry["secret"] == nil || entry["secret"] == "" {
		t.Fatal("file must persist the secret")
	}
}

func TestRevokeByIDOrNameAndRevokeAll(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.Upsert("id-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert("id-2", "Nova", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke("Pixel"); err != nil {
		t.Fatalf("revoke by name: %v", err)
	}
	if store.Count() != 1 {
		t.Fatalf("expected 1 left, got %d", store.Count())
	}
	if err := store.Revoke("id-2"); err != nil {
		t.Fatalf("revoke by id: %v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("expected empty, got %d", store.Count())
	}
	if _, err := store.Upsert("id-3", "Keep", now); err != nil {
		t.Fatal(err)
	}
	laptop := store.LaptopID()
	if err := store.RevokeAll(); err != nil {
		t.Fatalf("revoke-all: %v", err)
	}
	if store.Count() != 0 {
		t.Fatal("revoke-all should clear phones")
	}
	if store.LaptopID() != laptop {
		t.Fatal("revoke-all must keep laptop_id")
	}
	if err := store.Revoke("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPutWritesProvidedSecretWithoutReminting(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := store.Put("phone-1", "Pixel", "given-secret", now); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := store.LookupBySecret("phone-1", "given-secret")
	if !ok || got.Name != "Pixel" {
		t.Fatalf("Put must store the provided secret, got %#v ok=%v", got, ok)
	}
}

func TestOpenRoundTripsExistingFile(t *testing.T) {
	fs := newFakeFS()
	fs.env["XDG_CONFIG_HOME"] = "/cfg"
	store, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	secret, err := store.Upsert("phone-1", "Pixel", now)
	if err != nil {
		t.Fatal(err)
	}
	laptop := store.LaptopID()

	again, err := Open(fs, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if again.LaptopID() != laptop {
		t.Fatal("laptop_id should be stable")
	}
	if _, ok := again.LookupBySecret("phone-1", secret); !ok {
		t.Fatal("reopened store should still match the secret")
	}
}
