package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/illegalstudio/prompt-library/internal/syncgit"
)

type libraryStatus struct {
	Root        string          `json:"root"`
	Project     string          `json:"project"`
	Initialized bool            `json:"initialized"`
	Notes       int             `json:"notes"`
	Warnings    []string        `json:"warnings"`
	Git         *syncgit.Status `json:"git,omitempty"`
}

func newStatusCommand(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "status", Short: "Inspect local library and cached Git status without synchronizing", Args: cobra.NoArgs}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print local library status as JSON")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result := libraryStatus{Root: a.store.Root, Project: a.project, Initialized: a.repo.Configured(), Warnings: []string{}}
		if result.Initialized {
			notes, warnings, err := a.store.List("", "", true)
			if err != nil {
				return err
			}
			result.Notes, result.Warnings = len(notes), warnings
			git, err := a.repo.Status()
			if err != nil {
				return err
			}
			result.Git = &git
		}
		if asJSON {
			return json.NewEncoder(a.out).Encode(result)
		}
		fmt.Fprintln(a.out, "Library:", result.Root)
		fmt.Fprintln(a.out, "Initialized:", result.Initialized)
		fmt.Fprintln(a.out, "Notes:", result.Notes)
		if result.Git != nil {
			remote := result.Git.Remote
			if remote == "" {
				remote = "Local only"
			}
			fmt.Fprintln(a.out, "Remote:", remote)
			fmt.Fprintln(a.out, "Branch:", result.Git.Branch)
			fmt.Fprintln(a.out, "Uncommitted changes:", result.Git.Dirty)
			fmt.Fprintln(a.out, "Merge in progress:", result.Git.MergeInProgress)
			if result.Git.Ahead != nil {
				fmt.Fprintf(a.out, "Cached upstream: %s (%d ahead, %d behind)\n", result.Git.Upstream, *result.Git.Ahead, *result.Git.Behind)
			}
		}
		for _, warning := range result.Warnings {
			fmt.Fprintln(a.errOut, warning)
		}
		return nil
	}
	return cmd
}
