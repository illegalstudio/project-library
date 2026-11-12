package syncgit

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Status is local metadata only. Tracking counts use the last fetched refs.
type Status struct {
	Remote          string `json:"remote"`
	Branch          string `json:"branch"`
	HasCommit       bool   `json:"has_commit"`
	Dirty           bool   `json:"dirty"`
	MergeInProgress bool   `json:"merge_in_progress"`
	Upstream        string `json:"upstream"`
	Ahead           *int   `json:"ahead"`
	Behind          *int   `json:"behind"`
}

// Status neither fetches nor changes the index, working tree or Git config.
func (r Repo) Status() (Status, error) {
	s := Status{Remote: r.Remote()}
	b, err := r.git("--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return s, err
	}
	s.Dirty = len(b) > 0
	b, _ = r.git("symbolic-ref", "--quiet", "--short", "HEAD")
	s.Branch = strings.TrimSpace(string(b))
	_, err = r.git("rev-parse", "--verify", "HEAD")
	s.HasCommit = err == nil
	if _, err := os.Stat(filepath.Join(r.Root, ".git", "MERGE_HEAD")); err == nil {
		s.MergeInProgress = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	b, err = r.git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return s, nil
	}
	s.Upstream = strings.TrimSpace(string(b))
	b, err = r.git("rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return s, err
	}
	counts := strings.Fields(string(b))
	if len(counts) != 2 {
		return s, errors.New("invalid Git tracking counts")
	}
	ahead, err := strconv.Atoi(counts[0])
	if err != nil {
		return s, err
	}
	behind, err := strconv.Atoi(counts[1])
	if err != nil {
		return s, err
	}
	s.Ahead, s.Behind = &ahead, &behind
	return s, nil
}
