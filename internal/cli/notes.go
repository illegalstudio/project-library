package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/illegalstudio/prompt-library/internal/store"
)

type noteView struct {
	store.Note
	Revision string `json:"revision"`
}

func noteJSON(p store.Note) noteView { return noteView{Note: p, Revision: store.Revision(p)} }

func notesJSON(notes []store.Note) []noteView {
	out := make([]noteView, 0, len(notes))
	for _, p := range notes {
		out = append(out, noteJSON(p))
	}
	return out
}

type syncResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type writeResult struct {
	Operation string     `json:"operation"`
	Note      *noteView  `json:"note,omitempty"`
	Deleted   bool       `json:"deleted"`
	Sync      syncResult `json:"sync"`
	Error     string     `json:"error,omitempty"`
}

func (a app) syncResult() syncResult {
	if a.noSync {
		return syncResult{Status: "skipped"}
	}
	if err := a.repo.Sync(); err != nil {
		a.warn(err)
		return syncResult{Status: "pending", Error: err.Error()}
	}
	return syncResult{Status: "complete"}
}

func (a app) writeOutput(asJSON bool, result writeResult, err error) error {
	if err != nil {
		result.Error = err.Error()
	}
	if asJSON {
		if encodeErr := json.NewEncoder(a.out).Encode(result); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
	} else if err == nil {
		verb := "Saved"
		if result.Deleted {
			verb = "Deleted"
		}
		fmt.Fprintln(a.out, verb, result.Note.ID)
	}
	return err
}

func newAddCommand(a *app) *cobra.Command {
	var title, body string
	var global, projectOnly, stdin, asJSON bool
	cmd := &cobra.Command{Use: "add", Short: "Add a note, or open the multiline form", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&title, "title", "", "Note title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Note body (may be empty)")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "Read the complete body from standard input")
	cmd.Flags().BoolVar(&global, "global", false, "Save globally (default)")
	cmd.Flags().BoolVar(&projectOnly, "project", false, "Save for the current Git remote")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the saved note and synchronization result as JSON")
	cmd.MarkFlagsMutuallyExclusive("global", "project")
	cmd.MarkFlagsMutuallyExclusive("body", "stdin")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result := writeResult{Operation: "add", Sync: syncResult{Status: "skipped"}}
		err := func() error {
			if err := a.ready(); err != nil {
				return err
			}
			if projectOnly && a.project == "" {
				return errors.New("project notes require a Git repository with a portable remote URL")
			}
			if !cmd.Flags().Changed("title") && !stdin && !cmd.Flags().Changed("body") && !asJSON {
				if !interactiveCommand(cmd) {
					return errors.New("use --title outside a terminal; the body is optional")
				}
				m := newModel(*a)
				m.beginAdd()
				m.projectScope = projectOnly
				_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(a.out)).Run()
				return err
			}
			if stdin {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				body = string(b)
			}
			scope := ""
			if projectOnly {
				scope = a.project
			}
			p, err := store.New(title, body, scope)
			if err != nil {
				return err
			}
			return a.repo.Locked(func() error {
				if err := a.store.Save(p); err != nil {
					return err
				}
				result.Sync = a.syncResult()
				current, err := a.store.Find(p.ID)
				if err != nil {
					return err
				}
				view := noteJSON(current)
				result.Note = &view
				return nil
			})
		}()
		// The interactive form already reported its result.
		if !asJSON && result.Note == nil && err == nil {
			return nil
		}
		return a.writeOutput(asJSON, result, err)
	}
	return cmd
}

func newEditCommand(a *app) *cobra.Command {
	var title, body, revision string
	var stdin, global, projectOnly, asJSON bool
	cmd := &cobra.Command{Use: "edit ID", Short: "Edit a note without opening the interactive interface", Args: cobra.ExactArgs(1)}
	cmd.Flags().StringVar(&title, "title", "", "Replace the title")
	cmd.Flags().StringVar(&body, "body", "", "Replace the body (an empty string clears it)")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "Replace the body with the complete standard input")
	cmd.Flags().BoolVar(&global, "global", false, "Move the note to global scope")
	cmd.Flags().BoolVar(&projectOnly, "project", false, "Move the note to the current Git remote")
	cmd.Flags().StringVar(&revision, "if-match", "", "Require the revision returned by list/show --json")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the saved note and synchronization result as JSON")
	cmd.MarkFlagsMutuallyExclusive("global", "project")
	cmd.MarkFlagsMutuallyExclusive("body", "stdin")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result := writeResult{Operation: "edit", Sync: syncResult{Status: "skipped"}}
		err := func() error {
			if err := a.ready(); err != nil {
				return err
			}
			if !cmd.Flags().Changed("title") && !cmd.Flags().Changed("body") && !stdin && !global && !projectOnly {
				return errors.New("choose --title, --body, --stdin, --global or --project")
			}
			if cmd.Flags().Changed("if-match") && revision == "" {
				return errors.New("--if-match requires a nonempty revision")
			}
			if projectOnly && a.project == "" {
				return errors.New("project notes require a Git repository with a portable remote URL")
			}
			if stdin {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				body = string(b)
			}
			return a.repo.Locked(func() error {
				result.Sync = a.syncResult()
				original, err := a.store.Find(args[0])
				if err != nil {
					return err
				}
				view := noteJSON(original)
				result.Note = &view
				if revision != "" && revision != view.Revision {
					return errors.New("note changed; read it again before editing (--if-match mismatch)")
				}
				updated := original
				if cmd.Flags().Changed("title") {
					updated.Title = strings.TrimSpace(title)
				}
				if cmd.Flags().Changed("body") || stdin {
					updated.Body = body
				}
				if global {
					updated.Project = ""
				} else if projectOnly {
					updated.Project = a.project
				}
				updated.Updated = time.Now().UTC()
				if !updated.Updated.After(original.Updated) {
					updated.Updated = original.Updated.Add(time.Nanosecond)
				}
				if err := a.store.Replace(original, updated); err != nil {
					return err
				}
				result.Sync = a.syncResult()
				current, err := a.store.Find(updated.ID)
				if err != nil {
					return err
				}
				view = noteJSON(current)
				return nil
			})
		}()
		return a.writeOutput(asJSON, result, err)
	}
	return cmd
}

func newDeleteCommand(a *app) *cobra.Command {
	var yes, asJSON bool
	var revision string
	cmd := &cobra.Command{Use: "delete ID", Aliases: []string{"rm", "remove"}, Short: "Delete a note with explicit --yes confirmation", Args: cobra.ExactArgs(1)}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm deletion without an interactive prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the deletion and synchronization result as JSON")
	cmd.Flags().StringVar(&revision, "if-match", "", "Require the revision returned by list/show --json")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result := writeResult{Operation: "delete", Sync: syncResult{Status: "skipped"}}
		err := func() error {
			if !yes {
				return errors.New("deletion requires --yes; use the interactive interface to review a confirmation")
			}
			if err := a.ready(); err != nil {
				return err
			}
			if cmd.Flags().Changed("if-match") && revision == "" {
				return errors.New("--if-match requires a nonempty revision")
			}
			return a.repo.Locked(func() error {
				result.Sync = a.syncResult()
				p, err := a.store.Find(args[0])
				if err != nil {
					return err
				}
				view := noteJSON(p)
				result.Note = &view
				if revision != "" && revision != view.Revision {
					return errors.New("note changed; read it again before deleting (--if-match mismatch)")
				}
				if err := a.store.Delete(p); err != nil {
					return err
				}
				result.Sync = a.syncResult()
				if current, err := a.store.Find(p.ID); err == nil {
					view = noteJSON(current)
					return errors.New("note changed during sync and was kept; review it before deleting again")
				} else if !errors.Is(err, store.ErrNotFound) {
					return err
				}
				result.Deleted = true
				return nil
			})
		}()
		return a.writeOutput(asJSON, result, err)
	}
	return cmd
}
