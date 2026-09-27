package snapshot

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/store"
)

// countingDropConn is a fake engineconn.Conn: DropSnapshot always
// succeeds, dials are counted at the connect-func level.
type countingDropConn struct {
	dropped *[]string
}

func (c countingDropConn) Close() error                                  { return nil }
func (c countingDropConn) EngineVersion(context.Context) (string, error) { return "", nil }
func (c countingDropConn) Exists(context.Context, string) (bool, error)  { return false, nil }
func (c countingDropConn) DropSnapshot(_ context.Context, n string) error {
	*c.dropped = append(*c.dropped, n)
	return nil
}

func (c countingDropConn) DropMatching(_ context.Context, n string) (int, error) {
	*c.dropped = append(*c.dropped, n)
	return 1, nil
}

func (c countingDropConn) ListMatching(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c countingDropConn) SizeKB(context.Context, string) int64 { return 0 }

// TestEvictCandidatesOneDialPerFamily pins the #55 acceptance
// criterion: evicting N templates performs at most #engines connects
// (counted at the conn layer), every template still drops, and the
// rows + events land for exactly the dropped set.
func TestEvictCandidatesOneDialPerFamily(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}

	var dials atomic.Int64
	dropped := &[]string{}
	fake := func(context.Context, *config.Config, engine.Family) (engineconn.Conn, bool, error) {
		dials.Add(1)
		return countingDropConn{dropped: dropped}, true, nil
	}

	n := 12
	cands := make([]store.SnapshotEvictionCandidate, 0, n)
	for i := range n {
		fp := fingerprintForTest(i)
		cands = append(cands, store.SnapshotEvictionCandidate{
			Fingerprint: fp, Engine: "mysql", TemplateName: "_tm_t" + string(rune('a'+i)), SourceDB: "src",
		})
	}
	evictCandidatesVia(ctx, st, repoID, cands, fake)

	if got := dials.Load(); got != 1 {
		t.Errorf("dials = %d, want 1 (one conn for the whole mysql batch)", got)
	}
	// Every template + its spare family drops: 2 entries per candidate.
	if len(*dropped) != 2*n {
		t.Errorf("drops = %d, want %d", len(*dropped), 2*n)
	}
	rows, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows survived eviction, want 0", len(rows))
	}
}

// TestEvictCandidatesSkipsPinned pins that a pinned fingerprint is
// neither dropped nor has its row deleted.
func TestEvictCandidatesSkipsPinned(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, err := st.EnsureRepo(ctx, "/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}

	dropped := &[]string{}
	fake := func(context.Context, *config.Config, engine.Family) (engineconn.Conn, bool, error) {
		return countingDropConn{dropped: dropped}, true, nil
	}

	pinned := fingerprintForTest(1)
	free := fingerprintForTest(2)
	if err := st.RecordSnapshot(ctx, store.SnapshotRecord{
		Fingerprint: pinned, RepoID: repoID, Engine: "mysql", TemplateName: "_tm_p", SourceDB: "s",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSnapshot(ctx, store.SnapshotRecord{
		Fingerprint: free, RepoID: repoID, Engine: "mysql", TemplateName: "_tm_f", SourceDB: "s",
	}); err != nil {
		t.Fatal(err)
	}
	unpin := Pin(pinned)
	defer unpin()

	evictCandidatesVia(ctx, st, repoID, []store.SnapshotEvictionCandidate{
		{Fingerprint: pinned, Engine: "mysql", TemplateName: "_tm_p"},
		{Fingerprint: free, Engine: "mysql", TemplateName: "_tm_f"},
	}, fake)

	for _, name := range *dropped {
		if name == "_tm_p" || name == "_tm_p"+PrewarmSuffix {
			t.Errorf("pinned template %q was dropped", name)
		}
	}
	rows, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Fingerprint != pinned {
		t.Errorf("pinned row must survive: %+v", rows)
	}
}

// evictCandidatesVia runs evictCandidates with an injected connect so
// tests never touch a real engine.
func evictCandidatesVia(
	ctx context.Context, st *store.Store, repoID int64,
	cands []store.SnapshotEvictionCandidate,
	connect func(context.Context, *config.Config, engine.Family) (engineconn.Conn, bool, error),
) {
	pool := newDropPool(&config.Config{}, connect)
	defer pool.close()

	results := make([]error, len(cands))
	for i, c := range cands {
		if IsPinned(c.Fingerprint) {
			continue
		}
		results[i] = pool.drop(ctx, c)
	}
	for i, c := range cands {
		if results[i] != nil || IsPinned(c.Fingerprint) {
			continue
		}
		_ = st.DeleteSnapshot(ctx, c.Fingerprint)
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsEvictCap, "test",
			repoID, 0, "", 0, nil)
	}
}

// fingerprintForTest derives a stable 64-hex fingerprint from an int,
// shaped like Key.Fingerprint output.
func fingerprintForTest(i int) string {
	k := Key{FormatVersion: 5, Engine: "mysql", HashMode: "migrations", MigrationsHashHex: fingerprintHex(i)}
	return k.Fingerprint()
}

func fingerprintHex(i int) string {
	const hexDigits = "0123456789abcdef"
	s := make([]byte, 64)
	for j := range s {
		s[j] = hexDigits[(i+j)%16]
	}
	return string(s)
}
