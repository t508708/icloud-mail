package secure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShortAdminPathPersistsAcrossRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "keys", "admin-path")
	got, created, err := LoadOrCreateAdminPath(filename, "/admin/")
	if err != nil || !created || got != "/admin" {
		t.Fatalf("initial path = %q, created = %v, error = %v", got, created, err)
	}
	data, err := os.ReadFile(filename)
	if err != nil || string(data) != "/admin/\n" {
		t.Fatalf("persisted path = %q, error = %v", data, err)
	}
	got, created, err = LoadOrCreateAdminPath(filename, "")
	if err != nil || created || got != "/admin" {
		t.Fatalf("reloaded path = %q, created = %v, error = %v", got, created, err)
	}
	if _, _, err := LoadOrCreateAdminPath(filename, "/0123456789abcdef0123456789abcdef/admin"); err == nil {
		t.Fatal("a configured path change must require explicit persisted-path migration")
	}
}
