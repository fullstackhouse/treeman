// Named connection blocks: `connections.<family>` accepts either the
// single connection mapping (the historical shape, the implicit
// default) or a mapping of named blocks a `databases[].connection`
// selector picks from. This file holds the union decode, the
// per-family Resolve accessors, and the shape test that keeps the two
// YAML forms distinguishable.
package config

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stubbedev/treeman/internal/engine"
)

// connFieldNames lists the yaml keys a single connection block of type
// T accepts — the structural test that tells a single connection
// mapping apart from a named-blocks mapping without trying to decode
// either. A family key whose mapping contains ANY key outside this set
// is a named-blocks mapping. The set comes from yamlKeys, which mirrors
// the decoder's field rules (inline ContainerRef keys included), so it
// is exactly what T.UnmarshalYAML accepts. openEnded reports that T
// takes arbitrary keys (an inline catch-all), making every mapping a
// single block.
func connFieldNames[T any]() (fields map[string]bool, openEnded bool) {
	keys, openEnded := yamlKeys(reflect.TypeFor[T]())
	fields = make(map[string]bool, len(keys))
	for _, k := range keys {
		fields[k.Name] = true
	}
	return fields, openEnded
}

// decodeConnUnion fills `single` and/or `named` from one family key's
// node. Scalars decode as the single form (the conn types accept bare
// DSN strings); a mapping is single when every key is a field of the
// conn type, named otherwise.
func decodeConnUnion[T any](node *yaml.Node, single **T, named *map[string]*T) error {
	if isSingleConnNode[T](node) {
		return node.Decode(single)
	}
	m := map[string]*T{}
	if err := node.Decode(&m); err != nil {
		return errors.New(
			"expected a single connection block or named blocks (`<name>: {<conn fields>}`); the mapping matched neither shape: " + err.Error(),
		)
	}
	*named = m
	return nil
}

func isSingleConnNode[T any](node *yaml.Node) bool {
	if node.Kind != yaml.MappingNode {
		return true
	}
	fields, openEnded := connFieldNames[T]()
	if openEnded {
		return true
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if !fields[node.Content[i].Value] {
			return false
		}
	}
	return true
}

// UnmarshalYAML decodes the `connections:` block, routing each family
// key through the single-or-named union decode. ConnectionsConfig is
// exactly the seven family keys, so the mapping is walked directly —
// decoding through an alias-with-shadowed-fields breaks on scalar DSN
// values — and any other key fails loud, matching the decoder's
// KnownFields contract.
func (c *ConnectionsConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("connections: expected a mapping of family keys")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case "mysql":
			err = decodeConnUnion(val, &c.Mysql, &c.MysqlNamed)
		case "postgres":
			err = decodeConnUnion(val, &c.Postgres, &c.PostgresNamed)
		case "mongodb":
			err = decodeConnUnion(val, &c.Mongodb, &c.MongodbNamed)
		case "redis":
			err = decodeConnUnion(val, &c.Redis, &c.RedisNamed)
		case "elasticsearch":
			err = decodeConnUnion(val, &c.Elasticsearch, &c.ElasticsearchNamed)
		case "s3":
			err = decodeConnUnion(val, &c.S3, &c.S3Named)
		case "sqlite":
			err = decodeConnUnion(val, &c.Sqlite, &c.SqliteNamed)
		default:
			err = fmt.Errorf("unknown key %q (expected one of: mysql, postgres, mongodb, redis, elasticsearch, s3, sqlite)", key)
		}
		if err != nil {
			return fmt.Errorf("connections.%s: %w", key, err)
		}
	}
	return nil
}

// resolveNamed is the shared lookup behind the per-family Resolve
// methods: an empty name picks the singular default (nil = not
// configured — the caller decides whether that's a skip), a non-empty
// name must match a named block exactly.
func resolveNamed[T any](single *T, named map[string]*T, family, name string) (*T, error) {
	if name == "" {
		return single, nil
	}
	conn, ok := named[name]
	if !ok {
		return nil, fmt.Errorf("connections.%s: no connection named %q", family, name)
	}
	return conn, nil
}

// ResolveMysql returns the mysql connection a `connection: <name>`
// selector (or the singular default) points at.
func (c *ConnectionsConfig) ResolveMysql(name string) (*MysqlConn, error) {
	return resolveNamed(c.Mysql, c.MysqlNamed, "mysql", name)
}

// ResolvePostgres is ResolveMysql for postgres.
func (c *ConnectionsConfig) ResolvePostgres(name string) (*PostgresConn, error) {
	return resolveNamed(c.Postgres, c.PostgresNamed, "postgres", name)
}

// ResolveMongodb is ResolveMysql for mongodb.
func (c *ConnectionsConfig) ResolveMongodb(name string) (*MongoConn, error) {
	return resolveNamed(c.Mongodb, c.MongodbNamed, "mongodb", name)
}

// ResolveRedis is ResolveMysql for redis.
func (c *ConnectionsConfig) ResolveRedis(name string) (*RedisConn, error) {
	return resolveNamed(c.Redis, c.RedisNamed, "redis", name)
}

// ResolveElasticsearch is ResolveMysql for elasticsearch.
func (c *ConnectionsConfig) ResolveElasticsearch(name string) (*EsConn, error) {
	return resolveNamed(c.Elasticsearch, c.ElasticsearchNamed, "elasticsearch", name)
}

// ResolveS3 is ResolveMysql for s3.
func (c *ConnectionsConfig) ResolveS3(name string) (*S3Conn, error) {
	return resolveNamed(c.S3, c.S3Named, "s3", name)
}

// ResolveSqlite is ResolveMysql for the file family (the resolved
// block's base_dir relocates the rendered database files).
func (c *ConnectionsConfig) ResolveSqlite(name string) (*SqliteConn, error) {
	return resolveNamed(c.Sqlite, c.SqliteNamed, "sqlite", name)
}

// familyShape reports the singular/named shape of one family's
// connections block — the cross-check input for a database entry's
// `connection:` selector.
func (c *ConnectionsConfig) familyShape(fam engine.Family) (hasSingle bool, namedCount int) {
	switch fam {
	case engine.FamilyMySQL:
		return c.Mysql != nil, len(c.MysqlNamed)
	case engine.FamilyPostgres:
		return c.Postgres != nil, len(c.PostgresNamed)
	case engine.FamilyMongo:
		return c.Mongodb != nil, len(c.MongodbNamed)
	case engine.FamilyRedis:
		return c.Redis != nil, len(c.RedisNamed)
	case engine.FamilyES:
		return c.Elasticsearch != nil, len(c.ElasticsearchNamed)
	case engine.FamilyS3:
		return c.S3 != nil, len(c.S3Named)
	case engine.FamilyFile:
		return c.Sqlite != nil, len(c.SqliteNamed)
	default:
		return false, 0
	}
}

// hasNamedBlock reports whether `name` exists among the family's
// named blocks.
func (c *ConnectionsConfig) hasNamedBlock(fam engine.Family, name string) bool {
	switch fam {
	case engine.FamilyMySQL:
		_, ok := c.MysqlNamed[name]
		return ok
	case engine.FamilyPostgres:
		_, ok := c.PostgresNamed[name]
		return ok
	case engine.FamilyMongo:
		_, ok := c.MongodbNamed[name]
		return ok
	case engine.FamilyRedis:
		_, ok := c.RedisNamed[name]
		return ok
	case engine.FamilyES:
		_, ok := c.ElasticsearchNamed[name]
		return ok
	case engine.FamilyS3:
		_, ok := c.S3Named[name]
		return ok
	case engine.FamilyFile:
		_, ok := c.SqliteNamed[name]
		return ok
	default:
		return false
	}
}

// validateConnectionSelector checks one database entry's
// `connection:` selector against the family's declared blocks. Runs
// after the per-entry engine validation, so an unknown engine is
// already reported and skipped here.
func (c *Config) validateConnectionSelector(path string, d *DatabaseConfig) error {
	fam, ok := engine.Canonical(d.Engine)
	if !ok {
		return nil
	}
	hasSingle, namedCount := c.Connections.familyShape(fam)
	switch {
	case d.Connection == "" && !hasSingle && namedCount > 0:
		names := make([]string, 0, namedCount)
		for name := range c.Connections.namedNames(fam) {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf(
			"%s: engine %q declares only named connections — set connection: <name> (declared: %s)",
			path, d.Engine, strings.Join(names, ", "))
	case d.Connection != "" && namedCount == 0:
		return fmt.Errorf(
			"%s: connection %q selected but connections have no named blocks for engine %q (declare them as connections.<family>.%s: {...})",
			path,
			d.Connection,
			d.Engine,
			d.Connection,
		)
	case d.Connection != "" && !c.Connections.hasNamedBlock(fam, d.Connection):
		return fmt.Errorf(
			"%s: no connections block named %q for engine %q",
			path, d.Connection, d.Engine)
	}
	return nil
}

// namedNames lists a family's named block names (for error messages).
func (c *ConnectionsConfig) namedNames(fam engine.Family) map[string]struct{} {
	out := map[string]struct{}{}
	switch fam {
	case engine.FamilyMySQL:
		addNames(c.MysqlNamed, out)
	case engine.FamilyPostgres:
		addNames(c.PostgresNamed, out)
	case engine.FamilyMongo:
		addNames(c.MongodbNamed, out)
	case engine.FamilyRedis:
		addNames(c.RedisNamed, out)
	case engine.FamilyES:
		addNames(c.ElasticsearchNamed, out)
	case engine.FamilyS3:
		addNames(c.S3Named, out)
	case engine.FamilyFile:
		addNames(c.SqliteNamed, out)
	}
	return out
}

func addNames[T any](m map[string]*T, out map[string]struct{}) {
	for name := range m {
		out[name] = struct{}{}
	}
}
