package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNamedConnectionsDecodeAndResolve covers the two YAML shapes per
// family key and the selector resolution: a mapping whose keys are all
// connection fields is the historical single form; anything else is a
// named-blocks mapping a `databases[].connection` selector picks from.
func TestNamedConnectionsDecodeAndResolve(t *testing.T) {
	doc := `
connections:
  postgres:
    app:
      host: db-main
      user: app
    analytics:
      host: db-reporting
      user: app
  redis: redis://localhost:6379/0
databases:
  - engine: postgres
    name_template: "app_{slug}"
    connection: app
  - engine: redis
    key_prefix: "q:{slug}:"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	app, err := cfg.Connections.ResolvePostgres("app")
	if err != nil || app.Host != "db-main" || app.User != "app" {
		t.Errorf("ResolvePostgres(app) = %+v, %v", app, err)
	}
	if len(cfg.Connections.PostgresNamed) != 2 {
		t.Errorf("PostgresNamed = %v", cfg.Connections.PostgresNamed)
	}
	// The singular redis block still resolves under the empty name.
	if r, err := cfg.Connections.ResolveRedis(""); err != nil || r == nil {
		t.Errorf("ResolveRedis(\"\") = %v, %v", r, err)
	}
	// Unknown names fail loud.
	if _, err := cfg.Connections.ResolvePostgres("nope"); err == nil {
		t.Error("unknown selector accepted")
	}
	// The schema's engine enum + docs flow come from these very
	// structs — the named maps must never leak into reflection.
	if cfg.Connections.Postgres != nil {
		t.Error("named blocks must not populate the singular field")
	}
}

func TestNamedConnectionsValidate(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "named-only family requires a selector",
			doc: `
connections:
  postgres:
    app: { host: db-main, user: app }
databases:
  - engine: postgres
    name_template: "app_{slug}"
`,
			want: "declares only named connections",
		},
		{
			name: "selector must name an existing block",
			doc: `
connections:
  postgres:
    app: { uri: "postgres://app@db/app" }
databases:
  - engine: postgres
    name_template: "app_{slug}"
    connection: nope
`,
			want: `no connections block named "nope"`,
		},
		{
			name: "selector without any named blocks rejected",
			doc: `
connections:
  postgres: { host: db-main, user: app }
databases:
  - engine: postgres
    name_template: "app_{slug}"
    connection: app
`,
			want: "no named blocks",
		},
		{
			name: "singular + selector-free entry stays valid",
			doc: `
connections:
  postgres: { host: db-main, user: app }
databases:
  - engine: postgres
    name_template: "app_{slug}"
`,
			want: "",
		},
		{
			name: "two entries, two servers",
			doc: `
connections:
  postgres:
    app: { host: db-main, user: app }
    reporting: { host: db-reporting, user: app }
databases:
  - engine: postgres
    name_template: "app_{slug}"
    connection: app
  - engine: postgres
    name_template: "bi_{slug}"
    connection: reporting
`,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(c.doc), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			err := cfg.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && err == nil:
				t.Fatalf("expected error containing %q", c.want)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("error %q should contain %q", err, c.want)
			}
		})
	}
}

// TestNamedDecodeAmbiguity pins the structural single-vs-named test:
// a connection block that legitimately contains every conn field must
// decode as the SINGLE form even though it is also a valid mapping.
func TestNamedDecodeAmbiguity(t *testing.T) {
	doc := `
connections:
  mysql:
    host: db-main
    port: 3306
databases:
  - engine: mysql
    name_template: "app_{slug}"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Connections.Mysql == nil || cfg.Connections.Mysql.Host != "db-main" {
		t.Fatalf("single form misrouted: %+v / %+v", cfg.Connections.Mysql, cfg.Connections.MysqlNamed)
	}
	if len(cfg.Connections.MysqlNamed) != 0 {
		t.Errorf("single form leaked into named map: %v", cfg.Connections.MysqlNamed)
	}
}

// TestSingleConnBlockWithContainerRef pins #116: the ContainerRef keys
// (`container`, `compose_service`, ...) are inlined into every
// server-backed connection type, so a single block that uses them must
// still decode as the SINGLE form — flow style or block style.
func TestSingleConnBlockWithContainerRef(t *testing.T) {
	const ref = `container: app-db, compose_service: db, compose_project: app, container_engine: podman, container_network: backend`
	cases := []struct {
		family string
		block  string
		single func(*ConnectionsConfig) (bool, int)
	}{
		{"mysql", `{host: 127.0.0.1, port: 3306, user: root, ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.Mysql != nil && c.Mysql.Container == "app-db", len(c.MysqlNamed)
		}},
		{"postgres", `{host: 127.0.0.1, user: app, ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.Postgres != nil && c.Postgres.Container == "app-db", len(c.PostgresNamed)
		}},
		{"mongodb", `{uri: "mongodb://localhost:27017", ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.Mongodb != nil && c.Mongodb.Container == "app-db", len(c.MongodbNamed)
		}},
		{"redis", `{url: "redis://localhost:6379/0", ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.Redis != nil && c.Redis.Container == "app-db", len(c.RedisNamed)
		}},
		{"elasticsearch", `{url: "http://localhost:9200", ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.Elasticsearch != nil && c.Elasticsearch.Container == "app-db", len(c.ElasticsearchNamed)
		}},
		{"s3", `{endpoint: "http://localhost:9000", ` + ref + `}`, func(c *ConnectionsConfig) (bool, int) {
			return c.S3 != nil && c.S3.Container == "app-db", len(c.S3Named)
		}},
	}
	for _, c := range cases {
		t.Run(c.family, func(t *testing.T) {
			for _, doc := range []string{
				"connections:\n  " + c.family + ": " + c.block + "\n",
				flowToBlock(t, c.family, c.block),
			} {
				var cfg Config
				if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
					t.Fatalf("unmarshal %q: %v", doc, err)
				}
				single, named := c.single(&cfg.Connections)
				if !single || named != 0 {
					t.Fatalf("%q: single=%v named=%d, want the single form", doc, single, named)
				}
			}
		})
	}
}

// flowToBlock re-renders a flow-style connection block in block style
// so both YAML spellings run through the same shape test.
func flowToBlock(t *testing.T, family, flow string) string {
	t.Helper()
	var m yaml.Node
	if err := yaml.Unmarshal([]byte(flow), &m); err != nil {
		t.Fatalf("parse flow block: %v", err)
	}
	m.Content[0].Style = 0
	out, err := yaml.Marshal(map[string]map[string]*yaml.Node{"connections": {family: m.Content[0]}})
	if err != nil {
		t.Fatalf("render block style: %v", err)
	}
	return string(out)
}
