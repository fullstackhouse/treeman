// The explicit production driver list: every family treeman ships,
// wired to its connection block and dialer. Adding an engine = one
// entry here (+ the driver package + the schema/known-alias touchpoints
// documented in docs/internals.md, #47).
//
// Each Connect resolves (family, name) through the config accessors:
// an empty name picks the singular `connections.<family>` block, a
// non-empty name picks `connections.<family>.<name>` (#44).
package engineconn

import (
	"context"

	"github.com/stubbedev/treeman/internal/config"
	dbes "github.com/stubbedev/treeman/internal/db/es"
	dbmongo "github.com/stubbedev/treeman/internal/db/mongo"
	dbmysql "github.com/stubbedev/treeman/internal/db/mysql"
	dbpostgres "github.com/stubbedev/treeman/internal/db/postgres"
	dbredis "github.com/stubbedev/treeman/internal/db/redis"
	dbs3 "github.com/stubbedev/treeman/internal/db/s3"
	"github.com/stubbedev/treeman/internal/engine"
)

//nolint:gocognit,cyclop,funlen // a flat registration table: per-family branches live inside the Connect closures by design (#47)
func init() {
	Register(engine.FamilyFile, DriverFactory{
		// The file family has no server: it is always "configured" —
		// probe/drop/GC surfaces can act on rendered paths with no
		// connections block at all. A named block still selects the
		// base_dir for path resolution.
		Configured: func(*config.Config, string) bool { return true },
		Connect: func(context.Context, *config.Config, string) (Conn, bool, error) {
			return fileConn{}, true, nil
		},
	})
	Register(engine.FamilyMySQL, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolveMysql(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolveMysql(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbmysql.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return mysqlConn{d}, true, nil
		},
	})
	Register(engine.FamilyPostgres, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolvePostgres(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolvePostgres(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbpostgres.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return postgresConn{d}, true, nil
		},
	})
	Register(engine.FamilyMongo, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolveMongodb(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolveMongodb(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbmongo.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return mongoConn{d, ctx}, true, nil
		},
	})
	Register(engine.FamilyRedis, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolveRedis(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolveRedis(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbredis.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return redisConn{d}, true, nil
		},
	})
	Register(engine.FamilyES, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolveElasticsearch(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolveElasticsearch(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbes.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return esConn{d}, true, nil
		},
	})
	Register(engine.FamilyS3, DriverFactory{
		Configured: func(cfg *config.Config, name string) bool {
			conn, err := cfg.Connections.ResolveS3(name)
			return err == nil && conn != nil
		},
		Connect: func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error) {
			conn, err := cfg.Connections.ResolveS3(name)
			if err != nil {
				return nil, true, err
			}
			if conn == nil {
				return nil, false, nil
			}
			d, err := dbs3.Connect(ctx, *conn)
			if err != nil {
				return nil, true, err
			}
			return s3Conn{d}, true, nil
		},
	})
}
