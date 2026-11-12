package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/illegalstudio/prompt-library/internal/store"
	"github.com/illegalstudio/prompt-library/internal/syncgit"
)

func TestStatusDoesNotCreateAnUninitializedLibrary(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	out, _, err := run(t, home, "", "status", "--json")
	var status libraryStatus
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || status.Initialized || status.Git != nil {
		t.Fatal(out, err)
	}
	for _, path := range []string{home, home + ".lock"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("status created library state", path)
		}
	}
}

func TestStatusReadsDirtyLibraryWithUnavailableRemoteAndIndexLock(t *testing.T) {
	home := noteLibrary(t)
	if _, _, err := run(t, home, "", "remote", "set", filepath.Join(t.TempDir(), "missing.git"), "--no-sync"); err != nil {
		t.Fatal(err)
	}
	r := syncgit.Repo{Root: home}
	s := store.Store{Root: home}
	if err := r.Locked(func() error {
		for _, scope := range []string{"", "github.com/acme/other"} {
			note, _ := store.New("Empty body", "", scope)
			if err := s.Save(note); err != nil {
				return err
			}
		}
		return os.WriteFile(filepath.Join(home, "global", "broken.json"), []byte("{"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(home, ".git", "info", "exclude")
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(home, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, home, "", "status", "--json")
	var status libraryStatus
	if err != nil || stderr != "" || json.Unmarshal([]byte(out), &status) != nil {
		t.Fatal(out, stderr, err)
	}
	if !status.Initialized || status.Notes != 2 || len(status.Warnings) != 1 || status.Git == nil || !status.Git.Dirty || status.Git.HasCommit || status.Git.Ahead != nil || status.Git.Remote == "" {
		t.Fatal("incorrect local status", out)
	}
	if _, err := os.Stat(exclude); !os.IsNotExist(err) {
		t.Fatal("status initialized storage")
	}
	if b, err := os.ReadFile(lock); err != nil || string(b) != "retained" {
		t.Fatal("status changed the index lock", err)
	}
}
