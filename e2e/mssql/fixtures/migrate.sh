#!/usr/bin/env sh
# Stand-in for a framework's migrate CLI. Applies pending .sql files
# from fixtures/migrations/ in lexical order AND records each
# application in a `_treeman_e2e_migrations` ledger so re-runs skip
# the already-applied files. That ledger is what makes the
# incremental-from-ancestor path correct: cloning a cached template
# carries the partial ledger with it, so this script applies only the
# new files on top.
set -eu
: "${DB_DATABASE:?DB_DATABASE not set}"
container="${MSSQL_CONTAINER:-treeman-e2e-mssql}"
password="${MSSQL_PASSWORD:-treemanE2E!pw1}"
dir="$(dirname "$0")/migrations"

# -b makes sqlcmd exit non-zero on a T-SQL error; without it a failed
# migration is a silent success and the suite proves nothing. -C
# trusts the certificate the server generated for itself.
sqlcmd_in() {
  docker exec -i "$container" /opt/mssql-tools18/bin/sqlcmd \
    -S localhost -U sa -P "$password" -C -b -d "$DB_DATABASE" "$@"
}

# Ledger table. The IF NOT EXISTS guard is idempotent across cold +
# incremental runs.
sqlcmd_in <<SQL
IF OBJECT_ID('dbo._treeman_e2e_migrations', 'U') IS NULL
  CREATE TABLE dbo._treeman_e2e_migrations (name nvarchar(200) NOT NULL PRIMARY KEY);
SQL

for f in "$dir"/*.sql; do
  [ -e "$f" ] || continue
  name="$(basename "$f")"
  applied="$(sqlcmd_in -h -1 -W -Q \
    "SET NOCOUNT ON; SELECT COUNT(*) FROM dbo._treeman_e2e_migrations WHERE name = N'$name'")"
  # Anything that is not a count means the probe itself broke, and a
  # broken probe that reads as "already applied" is how a migration
  # silently never runs — which is the shape of the bug this `case`
  # exists to make loud.
  case "$applied" in
    0) ;;
    [1-9]*) continue ;;
    *)
      echo "migrate: ledger probe for $name returned '$applied', wanted a row count" >&2
      exit 1
      ;;
  esac
  sqlcmd_in <"$f"
  sqlcmd_in -Q "INSERT INTO dbo._treeman_e2e_migrations(name) VALUES (N'$name')"
done
