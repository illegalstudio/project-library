package syncgit

import "testing"

func TestStatusUsesCachedTrackingRefsWithoutSynchronizing(t *testing.T) {
	a, b, _, p := setupPair(t)
	local := p
	local.Title = "Unpushed local change"
	save(t, b, local)
	if err := b.Locked(b.commit); err != nil {
		t.Fatal(err)
	}
	remote := p
	remote.Body = "Remote change"
	save(t, a, remote)
	syncRepo(t, a)
	status, err := b.Status()
	if err != nil || status.Dirty || status.Upstream != "origin/main" || status.Ahead == nil || *status.Ahead != 1 || *status.Behind != 0 {
		t.Fatalf("status fetched or lost local commits: %+v %v", status, err)
	}
	if err := b.Locked(func() error { _, err := b.git("fetch", "origin"); return err }); err != nil {
		t.Fatal(err)
	}
	status, err = b.Status()
	if err != nil || status.Ahead == nil || *status.Ahead != 1 || *status.Behind != 1 {
		t.Fatalf("status ignored cached refs: %+v %v", status, err)
	}
}
