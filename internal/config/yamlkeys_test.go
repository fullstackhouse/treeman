package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type yamlKeysInner struct {
	Deep string `yaml:"deep"`
}

type yamlKeysEmbedded struct {
	Spliced       string `yaml:"spliced"`
	yamlKeysInner `yaml:",inline"`
}

type yamlKeysFixture struct {
	Tagged     string `yaml:"tagged,omitempty"`
	Untagged   string
	Skipped    string `yaml:"-"`
	unexported string //nolint:unused // pins that yaml.v3 skips it

	yamlKeysEmbedded `yaml:",inline"`
	Ptr              *struct {
		ViaPtr string `yaml:"via_ptr"`
	} `yaml:",inline"`
}

type yamlKeysUnmarshaler struct{}

func (*yamlKeysUnmarshaler) UnmarshalYAML(*yaml.Node) error { return nil }

// TestYamlKeysMirrorsDecoder checks yamlKeys against yaml.v3 itself:
// every key it lists decodes under KnownFields, and a key it omits is
// rejected — so the reflection can't drift from the decoder.
func TestYamlKeysMirrorsDecoder(t *testing.T) {
	keys, openEnded := yamlKeys(reflect.TypeFor[yamlKeysFixture]())
	if openEnded {
		t.Fatal("fixture has no catch-all, yet reported open-ended")
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.Name)
	}
	want := []string{"tagged", "untagged", "spliced", "deep", "via_ptr"}
	if !slices.Equal(names, want) {
		t.Fatalf("yamlKeys = %v, want %v", names, want)
	}
	decode := func(key string) error {
		dec := yaml.NewDecoder(strings.NewReader(key + ": x\n"))
		dec.KnownFields(true)
		var v yamlKeysFixture
		return dec.Decode(&v)
	}
	for _, key := range want {
		if err := decode(key); err != nil {
			t.Errorf("decoder rejects listed key %q: %v", key, err)
		}
	}
	for _, key := range []string{"skipped", "unexported", "yamlkeysembedded", "ptr"} {
		if decode(key) == nil {
			t.Errorf("decoder accepts unlisted key %q", key)
		}
	}
}

func TestYamlKeysOpenEnded(t *testing.T) {
	type withMap struct {
		Known string            `yaml:"known"`
		Rest  map[string]string `yaml:",inline"`
	}
	type withUnmarshaler struct {
		Inner yamlKeysUnmarshaler `yaml:",inline"`
	}
	if _, open := yamlKeys(reflect.TypeFor[withMap]()); !open {
		t.Error("inline map must report open-ended")
	}
	if _, open := yamlKeys(reflect.TypeFor[withUnmarshaler]()); !open {
		t.Error("inline Unmarshaler must report open-ended")
	}
}
