package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/illegalstudio/prompt-library/internal/store"
	"github.com/illegalstudio/prompt-library/internal/syncgit"
)

func noteLibrary(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home := filepath.Join(t.TempDir(), "library")
	if _, _, err := run(t, home, "", "init", "--local"); err != nil {
		t.Fatal(err)
	}
	return home
}

func decodeWrite(t *testing.T, out string) writeResult {
	t.Helper()
	var result writeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid write response %q: %v", out, err)
	}
	return result
}

func addNote(t *testing.T, home string) noteView {
	t.Helper()
	out, _, err := run(t, home, "", "add", "--title", "A note", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	result := decodeWrite(t, out)
	if result.Note == nil || result.Note.Body != "" || result.Note.Revision == "" || result.Sync.Status != "complete" {
		t.Fatalf("unexpected add result: %+v", result)
	}
	return *result.Note
}

func TestNoteAutomationAndRevisionChecks(t *testing.T) {
	home := noteLibrary(t)
	original := addNote(t, home)
	s := store.Store{Root: home}
	// Usage updates may happen while a note editor is open.
	used := original.Note
	used.LastUsed = time.Now().UTC()
	if err := (syncgit.Repo{Root: home}).Locked(func() error { return s.Save(used) }); err != nil {
		t.Fatal(err)
	}
	body := "first line\nsecond line\n"
	out, _, err := run(t, home, body, "edit", original.ID[:8], "--stdin", "--title", " Updated ", "--if-match", original.Revision, "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	edited := decodeWrite(t, out)
	if edited.Note.Title != "Updated" || edited.Note.Body != body || edited.Note.Revision == original.Revision || !edited.Note.Created.Equal(original.Created) || !edited.Note.LastUsed.Equal(used.LastUsed) {
		t.Fatalf("identity, usage or edit lost: %+v", edited.Note)
	}
	for _, args := range [][]string{
		{"edit", original.ID, "--body", "stale", "--if-match", original.Revision, "--json"},
		{"delete", original.ID, "--yes", "--if-match", original.Revision, "--json"},
		{"delete", original.ID, "--json"},
		{"edit", original.ID, "--json"},
		{"edit", original.ID, "--title", " ", "--json"},
		{"edit", original.ID, "--body", "bad", "--if-match", "", "--json"},
	} {
		out, _, err := run(t, home, "", args...)
		if err == nil || decodeWrite(t, out).Error == "" {
			t.Fatalf("unsafe operation succeeded: %v %s %v", args, out, err)
		}
		current, err := s.Find(original.ID)
		if err != nil || current.Title != "Updated" || current.Body != body {
			t.Fatal("failed operation changed the note", current, err)
		}
	}
	out, _, err = run(t, home, "", "edit", original.ID, "--body", "", "--json")
	if err != nil || decodeWrite(t, out).Note.Body != "" {
		t.Fatal("could not clear body", out, err)
	}
	out, _, err = run(t, home, "", "show", original.ID, "--json", "--no-sync")
	var current noteView
	if err != nil || json.Unmarshal([]byte(out), &current) != nil || current.Body != "" || current.Revision == "" {
		t.Fatal(out, err)
	}
	out, _, err = run(t, home, "", "delete", original.ID[:8], "--yes", "--if-match", current.Revision, "--json")
	deleted := decodeWrite(t, out)
	if err != nil || !deleted.Deleted || deleted.Note.ID != original.ID {
		t.Fatal(out, err)
	}
	if _, err := s.Find(original.ID); err == nil {
		t.Fatal("deleted note remains")
	}
	if out, _, err := run(t, home, "", "list", "--json", "--no-sync"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatal("empty JSON list is not an array", out, err)
	}
}

func TestConcurrentConditionalEditsHaveOneWinner(t *testing.T) {
	home := noteLibrary(t)
	note := addNote(t, home)
	type outcome struct {
		out string
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, body := range []string{"edit A", "edit B"} {
		go func(body string) {
			<-start
			out, _, err := run(t, home, "", "edit", note.ID, "--body", body, "--if-match", note.Revision, "--json", "--no-sync")
			results <- outcome{out, err}
		}(body)
	}
	close(start)
	wins := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			wins++
		} else if !strings.Contains(decodeWrite(t, result.out).Error, "note changed") {
			t.Fatal(result.out, result.err)
		}
	}
	if wins != 1 {
		t.Fatalf("conditional edits had %d winners", wins)
	}
}

func TestLocalReadsWithoutGitDoNotInitializeOrSync(t *testing.T) {
	home := noteLibrary(t)
	note := addNote(t, home)
	exclude := filepath.Join(home, ".git", "info", "exclude")
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"list", "--json", "--no-sync"},
		{"search", "note", "--json", "--no-sync"},
		{"show", note.ID, "--no-sync"},
		{"show", note.ID, "--json", "--no-sync"},
	} {
		if out, stderr, err := run(t, home, "", args...); err != nil || stderr != "" {
			t.Fatalf("offline read failed: %v %q %q %v", args, out, stderr, err)
		}
	}
	if _, err := os.Stat(exclude); !os.IsNotExist(err) {
		t.Fatal("offline read initialized Git metadata")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := run(t, missing, "", "list", "--no-sync"); err == nil {
		t.Fatal("uninitialized storage accepted")
	}
	for _, path := range []string{missing, missing + ".lock"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("local read created %s", path)
		}
	}
}

func TestWritesReportPendingAndSkippedSync(t *testing.T) {
	home := noteLibrary(t)
	remote := filepath.Join(t.TempDir(), "unavailable.git")
	if _, _, err := run(t, home, "", "remote", "set", remote, "--no-sync"); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, home, "", "add", "--title", "Offline", "--json")
	added := decodeWrite(t, out)
	if err != nil || added.Sync.Status != "pending" || added.Sync.Error == "" || !strings.Contains(stderr, "Sync pending") {
		t.Fatalf("offline write result: %s %q %v", out, stderr, err)
	}
	for _, args := range [][]string{
		{"edit", added.Note.ID, "--body", "local", "--json", "--no-sync"},
		{"delete", added.Note.ID, "--yes", "--json", "--no-sync"},
	} {
		out, stderr, err := run(t, home, "", args...)
		if err != nil || decodeWrite(t, out).Sync.Status != "skipped" || stderr != "" {
			t.Fatal(out, stderr, err)
		}
	}
}

func TestConditionalWritesRejectFetchedRemoteEdits(t *testing.T) {
	for _, operation := range []string{"edit", "delete"} {
		t.Run(operation, func(t *testing.T) {
			home := noteLibrary(t)
			note := addNote(t, home)
			base := t.TempDir()
			remote := filepath.Join(base, "remote.git")
			if b, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
				t.Fatal(string(b), err)
			}
			if _, _, err := run(t, home, "", "remote", "set", remote); err != nil {
				t.Fatal(err)
			}
			peer := syncgit.Repo{Root: filepath.Join(base, "peer")}
			changed := note.Note
			changed.Body = "A new remote edit"
			changed.Updated = changed.Updated.Add(time.Second)
			if err := peer.Locked(func() error {
				if err := peer.Init(); err != nil {
					return err
				}
				if err := peer.SetRemote(remote); err != nil {
					return err
				}
				if err := peer.Sync(); err != nil {
					return err
				}
				if err := (store.Store{Root: peer.Root}).Save(changed); err != nil {
					return err
				}
				return peer.Sync()
			}); err != nil {
				t.Fatal(err)
			}
			args := []string{operation, note.ID, "--if-match", note.Revision, "--json"}
			if operation == "edit" {
				args = append(args, "--body", "stale local edit")
			} else {
				args = append(args, "--yes")
			}
			out, _, err := run(t, home, "", args...)
			if err == nil || !strings.Contains(decodeWrite(t, out).Error, "note changed") {
				t.Fatal("remote revision change was ignored", out, err)
			}
			current, err := (store.Store{Root: home}).Find(note.ID)
			if err != nil || current.Body != changed.Body {
				t.Fatal("remote content was lost", current, err)
			}
		})
	}
}

func TestEditMovesScopeAndKeepsLegacyStorageShape(t *testing.T) {
	home := noteLibrary(t)
	note := addNote(t, home)
	project := t.TempDir()
	for _, args := range [][]string{{"init", "--initial-branch=main", project}, {"-C", project, "remote", "add", "origin", "https://github.com/acme/notes.git"}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	t.Chdir(project)
	s := store.Store{Root: home}
	for _, scope := range []string{"--project", "--global"} {
		out, _, err := run(t, home, "", "edit", note.ID, scope, "--json", "--no-sync")
		result := decodeWrite(t, out)
		if err != nil || result.Note.ID != note.ID || !result.Note.Created.Equal(note.Created) {
			t.Fatal(out, err)
		}
		if (scope == "--global") != (result.Note.Project == "") {
			t.Fatal("scope was not changed", out)
		}
		b, err := os.ReadFile(s.Path(result.Note.Note))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(b, &fields); err != nil || len(fields) < 6 || fields["id"] != note.ID {
			t.Fatal("legacy schema changed", string(b), err)
		}
		for _, key := range []string{"type", "kind", "revision"} {
			if _, exists := fields[key]; exists {
				t.Fatal("storage gained a discriminator or derived revision", key)
			}
		}
		notes, _, err := s.List("", "", true)
		if err != nil || len(notes) != 1 {
			t.Fatal("scope move duplicated notes", notes, err)
		}
	}
}

func TestTUISavesTitleOnlyNoteAndRejectsRevisionChanges(t *testing.T) {
	home := noteLibrary(t)
	a := app{store: store.Store{Root: home}, repo: syncgit.Repo{Root: home}, noSync: true}
	m := newModel(a)
	m.busy = false
	m.beginAdd()
	m.title.SetValue("Title only")
	_, cmd := m.save()
	result := cmd().(resultMsg)
	if result.err != nil || len(result.notes) != 1 || result.notes[0].Body != "" {
		t.Fatalf("title-only TUI save failed: %+v", result)
	}
	m.notes, m.mode = result.notes, "browse"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = next.(model)
	changed := result.notes[0]
	changed.Updated = changed.Updated.Add(time.Second)
	if err := a.repo.Locked(func() error { return a.store.Save(changed) }); err != nil {
		t.Fatal(err)
	}
	m.body.SetValue("stale editor")
	_, cmd = m.save()
	if result := cmd().(resultMsg); result.err == nil {
		t.Fatal("TUI ignored an intervening edit with restored content")
	}
}

func TestTUINoSyncAllowsExplicitSyncKey(t *testing.T) {
	home := noteLibrary(t)
	if _, _, err := run(t, home, "", "add", "--title", "Deferred note", "--no-sync"); err != nil {
		t.Fatal(err)
	}
	a := app{store: store.Store{Root: home}, repo: syncgit.Repo{Root: home}, noSync: true}
	m := newModel(a)
	// Loading an offline session leaves the deferred write uncommitted.
	if result := m.load(false)().(resultMsg); result.err != nil {
		t.Fatal(result.err)
	}
	status, err := a.repo.Status()
	if err != nil || !status.Dirty {
		t.Fatal("local load committed deferred writes", status, err)
	}
	m.busy = false
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("explicit sync was ignored")
	}
	if result := cmd().(resultMsg); result.err != nil || result.status != "Ready" {
		t.Fatalf("explicit sync failed: %+v", result)
	}
	status, err = a.repo.Status()
	if err != nil || status.Dirty || !status.HasCommit {
		t.Fatal("explicit sync did not commit deferred writes", status, err)
	}
}
