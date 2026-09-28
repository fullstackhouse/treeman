package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlKey is one mapping key a struct type decodes under, paired with
// the struct field that receives it.
type yamlKey struct {
	Name  string
	Field reflect.StructField
}

var yamlUnmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()

// yamlKeys lists the mapping keys yaml.v3 decodes into struct type t,
// mirroring the decoder's own field rules (yaml.v3 getStructInfo) so
// reflection over config types can never disagree with what a document
// actually accepts:
//
//   - unexported non-embedded fields are skipped;
//   - `yaml:"-"` is skipped;
//   - the key is the tag name before any comma, or the lower-cased
//     field name when the tag leaves it empty;
//   - `,inline` on a struct (or pointer-to-struct) field splices that
//     struct's keys in place, recursively;
//   - `,inline` on a map field, or on a struct whose pointer implements
//     yaml.Unmarshaler (it receives the whole mapping), accepts keys
//     no reflection can enumerate — reported via openEnded.
func yamlKeys(t reflect.Type) (keys []yamlKey, openEnded bool) {
	for f := range t.Fields() {
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if hasTagOpt(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch {
			case ft.Kind() == reflect.Map:
				openEnded = true
			case ft.Kind() == reflect.Struct && reflect.PointerTo(ft).Implements(yamlUnmarshalerType):
				openEnded = true
			case ft.Kind() == reflect.Struct:
				inner, innerOpen := yamlKeys(ft)
				keys = append(keys, inner...)
				openEnded = openEnded || innerOpen
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		keys = append(keys, yamlKey{Name: name, Field: f})
	}
	return keys, openEnded
}

// hasTagOpt reports whether the comma-separated struct-tag options
// contain opt.
func hasTagOpt(opts, opt string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == opt {
			return true
		}
	}
	return false
}
