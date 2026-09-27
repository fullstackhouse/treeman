package mysql

import (
	"database/sql"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// rowsFromPairs builds a real *sql.Rows yielding (table_name,
// column_name) in the given order, so scanColumnsByTable can be
// exercised exactly as it is against MySQL — without needing one.
// The ORDER BY in the production query is what guarantees the order
// reaching scanColumnsByTable; here the caller controls insertion +
// SELECT order to stand in for that guarantee.
// rowsFromPairs builds a real *sql.Rows yielding (table_name,
// column_name) in the given order, so scanColumnsByTable can be
// exercised exactly as it is against MySQL — without needing one.
// The ORDER BY in the production query is what guarantees the order
// reaching scanColumnsByTable; here the caller controls insertion +
// SELECT order to stand in for that guarantee.
func rowsFromPairs(t *testing.T, pairs [][2]string) (*sql.Rows, func()) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE c(seq INTEGER PRIMARY KEY, tn TEXT, cn TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, p := range pairs {
		if _, err := db.Exec(`INSERT INTO c(seq, tn, cn) VALUES (?,?,?)`, i, p[0], p[1]); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	rows, err := db.Query(`SELECT tn, cn FROM c ORDER BY seq`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return rows, func() { _ = rows.Close(); _ = db.Close() }
}

func TestScanColumnsByTable(t *testing.T) {
	// Interleaved across tables and out of insertion-friendly order to
	// prove grouping is by table while per-table order follows the row
	// stream (i.e. the query's ORDER BY TABLE_NAME, ORDINAL_POSITION).
	rows, cleanup := rowsFromPairs(t, [][2]string{
		{"users", "id"},
		{"users", "email"},
		{"posts", "id"},
		{"users", "created_at"},
		{"posts", "body"},
	})
	defer cleanup()

	got, err := scanColumnsByTable(rows)
	if err != nil {
		t.Fatalf("scanColumnsByTable: %v", err)
	}
	want := map[string][]string{
		"users": {"id", "email", "created_at"},
		"posts": {"id", "body"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grouping mismatch:\n got  %#v\n want %#v", got, want)
	}
}

func TestScanColumnsByTable_Empty(t *testing.T) {
	// A schema whose tables are all-generated yields zero rows; the
	// result must be an empty (non-nil) map so callers default each
	// missing table to an empty column list and skip its data copy.
	rows, cleanup := rowsFromPairs(t, nil)
	defer cleanup()

	got, err := scanColumnsByTable(rows)
	if err != nil {
		t.Fatalf("scanColumnsByTable: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil map, got %#v", got)
	}
}

func TestScanColumnsByTable_SingleColumnTable(t *testing.T) {
	rows, cleanup := rowsFromPairs(t, [][2]string{{"solo", "only_col"}})
	defer cleanup()

	got, err := scanColumnsByTable(rows)
	if err != nil {
		t.Fatalf("scanColumnsByTable: %v", err)
	}
	want := map[string][]string{"solo": {"only_col"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// TestPhysicalCloneMinBytes pins the threshold resolution order: the
// TREEMAN_MYSQL_PHYSICAL_MIN_BYTES env var (kept for e2e + ad-hoc
// tuning) overrides the database's configured
// `physical_clone_min_bytes`, which overrides the 1 GiB default.
func TestPhysicalCloneMinBytes(t *testing.T) {
	t.Run("default is 1 GiB", func(t *testing.T) {
		d := &Driver{}
		if got := d.physicalCloneMinBytes(); got != defaultPhysicalCloneMinBytes {
			t.Fatalf("got %d, want %d", got, defaultPhysicalCloneMinBytes)
		}
	})

	t.Run("configured threshold wins over default", func(t *testing.T) {
		zero := int64(0)
		d := &Driver{}
		d.SetPhysicalCloneMinBytes(&zero)
		if got := d.physicalCloneMinBytes(); got != 0 {
			t.Fatalf("got %d, want 0 (0 must stay reachable: it forces the physical path)", got)
		}
	})

	t.Run("env var overrides the configured threshold", func(t *testing.T) {
		t.Setenv("TREEMAN_MYSQL_PHYSICAL_MIN_BYTES", "4096")
		configured := int64(1 << 20)
		d := &Driver{}
		d.SetPhysicalCloneMinBytes(&configured)
		if got := d.physicalCloneMinBytes(); got != 4096 {
			t.Fatalf("got %d, want 4096", got)
		}
	})

	t.Run("unparseable env var falls through to config", func(t *testing.T) {
		t.Setenv("TREEMAN_MYSQL_PHYSICAL_MIN_BYTES", "not-a-number")
		configured := int64(1 << 20)
		d := &Driver{}
		d.SetPhysicalCloneMinBytes(&configured)
		if got := d.physicalCloneMinBytes(); got != 1<<20 {
			t.Fatalf("got %d, want %d", got, int64(1<<20))
		}
	})
}
