package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/illegalstudio/prompt-library/internal/project"
)

type Note struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Project  string    `json:"project,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	LastUsed time.Time `json:"last_used"`
}

type Store struct{ Root string }

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

var ErrNotFound = errors.New("note not found")

// Revision changes with note edits, including edits that restore earlier content.
// Clipboard usage does not invalidate an editor's revision.
func Revision(p Note) string {
	p.LastUsed = time.Time{}
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func DefaultRoot() (string, error) {
	if root := os.Getenv("PLIB_HOME"); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".plib"), nil
}

func New(title, body, scope string) (Note, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Note{}, err
	}
	now := time.Now().UTC()
	p := Note{ID: hex.EncodeToString(id), Title: strings.TrimSpace(title), Body: body, Project: scope, Created: now, Updated: now}
	return p, Validate(p)
}

func Validate(p Note) error {
	if !validID.MatchString(p.ID) {
		return errors.New("invalid note ID")
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("title is required")
	}
	return nil
}

func (s Store) Path(p Note) string {
	if p.Project == "" {
		return filepath.Join(s.Root, "global", p.ID+".json")
	}
	return filepath.Join(s.Root, "projects", project.Folder(p.Project), p.ID+".json")
}

func (s Store) Save(p Note) error {
	if err := Validate(p); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(s.Root, s.Path(p), append(b, '\n'))
}

// Delete removes one note. The caller must hold the repository lock.
func (s Store) Delete(p Note) error {
	if err := Validate(p); err != nil {
		return err
	}
	return os.Remove(s.Path(p))
}

// Replace preserves a note's identity when moving it between scope folders.
// The caller must hold the repository lock.
func (s Store) Replace(original, updated Note) error {
	if err := Validate(original); err != nil {
		return err
	}
	if original.ID != updated.ID {
		return errors.New("cannot change a note's ID")
	}
	oldPath, newPath := s.Path(original), s.Path(updated)
	if oldPath == newPath {
		return s.Save(updated)
	}
	if _, err := os.Lstat(newPath); err == nil {
		return errors.New("a note already exists in the destination scope")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.Save(updated); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		// Keep the original record if a scope move cannot finish.
		return fmt.Errorf("move note: %w", errors.Join(err, os.Remove(newPath)))
	}
	return nil
}

func AtomicWrite(root, path string, b []byte) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("write path is outside the library")
	}
	// Refuse directory symlinks inside the library, while allowing system
	// ancestors such as macOS /var, which points to /private/var.
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		st, err := os.Lstat(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink directory %s", dir)
		}
		if dir == root {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".plib-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// List skips damaged files so a single bad record does not hide the library.
func (s Store) List(scope, query string, all bool) ([]Note, []string, error) {
	notes := []Note{}
	warnings := []string{}
	for _, folder := range []string{"global", "projects"} {
		err := filepath.WalkDir(filepath.Join(s.Root, folder), func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				warnings = append(warnings, "Skipped symlink: "+path)
				return nil
			}
			b, e := os.ReadFile(path)
			var p Note
			if e == nil {
				e = json.Unmarshal(b, &p)
			}
			if e == nil {
				e = Validate(p)
			}
			if e == nil && filepath.Clean(s.Path(p)) != filepath.Clean(path) {
				e = errors.New("note path does not match its identity")
			}
			if e != nil {
				warnings = append(warnings, "Skipped invalid note: "+path)
				return nil
			}
			if !all && p.Project != "" && p.Project != scope {
				return nil
			}
			haystack := strings.ToLower(p.Title + "\n" + p.Body)
			for _, word := range strings.Fields(strings.ToLower(query)) {
				if !strings.Contains(haystack, word) {
					return nil
				}
			}
			notes = append(notes, p)
			return nil
		})
		if err != nil {
			return nil, warnings, err
		}
	}
	sort.Slice(notes, func(i, j int) bool {
		if !notes[i].LastUsed.Equal(notes[j].LastUsed) {
			return notes[i].LastUsed.After(notes[j].LastUsed)
		}
		if !notes[i].Updated.Equal(notes[j].Updated) {
			return notes[i].Updated.After(notes[j].Updated)
		}
		return notes[i].ID < notes[j].ID
	})
	return notes, warnings, nil
}

func (s Store) Find(id string) (Note, error) {
	if id == "" {
		return Note{}, errors.New("a nonempty note ID is required")
	}
	ps, _, err := s.List("", "", true)
	if err != nil {
		return Note{}, err
	}
	var matches []Note
	for _, p := range ps {
		if strings.HasPrefix(p.ID, id) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return Note{}, fmt.Errorf("ID %q matches 0 notes: %w", id, ErrNotFound)
	}
	if len(matches) != 1 {
		return Note{}, fmt.Errorf("ID %q matches %d notes; use a unique ID", id, len(matches))
	}
	return matches[0], nil
}
