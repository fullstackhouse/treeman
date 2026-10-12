package mssql

import (
	"context"
	"fmt"
	"strings"

	"github.com/stubbedev/treeman/internal/db/ident"
	"github.com/stubbedev/treeman/internal/db/rowcount"
)

// nonEmptyTables lists up to rowcount.SampleLimit of `db`'s smallest
// non-empty tables, as `schema.table`. Unlike MySQL and Postgres a
// table name alone is not unique within a database here — SQL Server
// has real schemas, and a vendor database puts the same table name in
// several — so the schema travels with it and countOf splits it back
// apart. Estimates only PICK the sample; exact counts decide.
func (d *Driver) nonEmptyTables(ctx context.Context, db string) ([]string, error) {
	qdb, err := ident.QuoteMSSQL(db)
	if err != nil {
		return nil, err
	}
	// Three-part names reach another database's catalog views, so this
	// runs on the master pool without a scoped connection. index_id
	// 0/1 is the heap or the clustered index — the one partition set
	// that holds the rows rather than a copy of them.
	rows, err := d.DB.QueryContext(ctx, fmt.Sprintf(`
		SELECT TOP (@p1) s.name, t.name
		FROM %[1]s.sys.tables t
		JOIN %[1]s.sys.schemas s ON s.schema_id = t.schema_id
		JOIN %[1]s.sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0, 1)
		GROUP BY s.name, t.name
		HAVING SUM(p.rows) > 0
		ORDER BY SUM(p.rows) ASC`, qdb), rowcount.SampleLimit)
	if err != nil {
		return nil, fmt.Errorf("row-count sample list %s: %w", db, err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, err
		}
		tables = append(tables, schema+"."+name)
	}
	return tables, rows.Err()
}

// countOf counts one `schema.table` with identifier validation and
// quoting on each of the three parts.
func (d *Driver) countOf(ctx context.Context, db, table string) (int64, error) {
	qdb, err := ident.QuoteMSSQL(db)
	if err != nil {
		return 0, err
	}
	schema, name, ok := strings.Cut(table, ".")
	if !ok {
		// A bare name can only have come from a caller that did not go
		// through nonEmptyTables; `dbo` is the only defensible guess.
		schema, name = "dbo", table
	}
	qschema, err := ident.QuoteMSSQL(schema)
	if err != nil {
		return 0, err
	}
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := d.DB.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT_BIG(*) FROM %s.%s.%s", qdb, qschema, qname)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// RowCountSample returns exact row counts for a small sample of `db`'s
// tables (see nonEmptyTables). Capture/restore verification compares
// the source's sample against the copy's; an empty map means "no table
// the catalog believes non-empty" and is NOT a verification failure.
func (d *Driver) RowCountSample(ctx context.Context, db string) (map[string]int64, error) {
	if err := ident.ValidateMSSQL(db); err != nil {
		return nil, err
	}
	return rowcount.Sample(ctx, d.nonEmptyTables, d.countOf, db)
}

// RowCountsFor returns exact row counts for the NAMED tables in `db`.
// Capture verification uses it to count the SOURCE's sample tables in
// the COPY — by name, so the copy's own catalog estimates never decide.
func (d *Driver) RowCountsFor(ctx context.Context, db string, tables []string) (map[string]int64, error) {
	if err := ident.ValidateMSSQL(db); err != nil {
		return nil, err
	}
	return rowcount.CountsFor(ctx, d.countOf, db, tables)
}
