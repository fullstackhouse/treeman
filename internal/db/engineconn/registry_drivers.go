// The explicit production driver list: every family treeman ships,
// wired to its connection block and dialer. Adding an engine = one
// entry here (+ the driver package + the schema/known-alias touchpoints
// documented in docs/internals.md, #47).
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

func init() {
	Register(engine.FamilyFile, DriverFactory{
		// The file family has no server: it is always "configured" —
		// probe/drop/GC surfaces can act on rendered paths with no
		// connections block at all.
		Configured: func(*config.Config) bool { return true },
		Connect: func(context.Context, *config.Config) (Conn, bool, error) {
			return fileConn{}, true, nil
		},
	})
	Register(engine.FamilyMySQL, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.Mysql != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.Mysql == nil {
				return nil, false, nil
			}
			d, err := dbmysql.Connect(ctx, *cfg.Connections.Mysql)
			if err != nil {
				return nil, true, err
			}
			return mysqlConn{d}, true, nil
		},
	})
	Register(engine.FamilyPostgres, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.Postgres != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.Postgres == nil {
				return nil, false, nil
			}
			d, err := dbpostgres.Connect(ctx, *cfg.Connections.Postgres)
			if err != nil {
				return nil, true, err
			}
			return postgresConn{d}, true, nil
		},
	})
	Register(engine.FamilyMongo, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.Mongodb != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.Mongodb == nil {
				return nil, false, nil
			}
			d, err := dbmongo.Connect(ctx, *cfg.Connections.Mongodb)
			if err != nil {
				return nil, true, err
			}
			return mongoConn{d, ctx}, true, nil
		},
	})
	Register(engine.FamilyRedis, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.Redis != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.Redis == nil {
				return nil, false, nil
			}
			d, err := dbredis.Connect(ctx, *cfg.Connections.Redis)
			if err != nil {
				return nil, true, err
			}
			return redisConn{d}, true, nil
		},
	})
	Register(engine.FamilyES, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.Elasticsearch != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.Elasticsearch == nil {
				return nil, false, nil
			}
			d, err := dbes.Connect(ctx, *cfg.Connections.Elasticsearch)
			if err != nil {
				return nil, true, err
			}
			return esConn{d}, true, nil
		},
	})
	Register(engine.FamilyS3, DriverFactory{
		Configured: func(cfg *config.Config) bool { return cfg.Connections.S3 != nil },
		Connect: func(ctx context.Context, cfg *config.Config) (Conn, bool, error) {
			if cfg.Connections.S3 == nil {
				return nil, false, nil
			}
			d, err := dbs3.Connect(ctx, *cfg.Connections.S3)
			if err != nil {
				return nil, true, err
			}
			return s3Conn{d}, true, nil
		},
	})
}
