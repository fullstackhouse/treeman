package framework

import (
	"testing"

	"github.com/stubbedev/treeman/internal/config"
)

// TestRegistryForCarriesCustomCommands pins the #74 mapping: a custom
// `frameworks:` entry declaring migrate/rollback commands gets them on
// its detection Spec, so `fw detect` reports them and `treeman init`
// scaffolds runnable migrate/rollback blocks instead of dropping the
// declaration.
func TestRegistryForCarriesCustomCommands(t *testing.T) {
	cfg := &config.Config{
		Frameworks: map[string]config.CustomFramework{
			"sequel": {
				Markers:       []string{"Rakefile"},
				MigrationDirs: []string{"db/migrations"},
				FilePattern:   "[0-9]*_*.rb",
				EngineHint:    "mysql",
				MigrateRun:    "bundle exec rake db:migrate",
				MigrateEnv:    map[string]string{"DB_NAME": "{target_db}"},
				RollbackRun:   "bundle exec rake db:rollback STEP=$TREEMAN_ROLLBACK_STEPS",
				RollbackEnv:   map[string]string{"DB_NAME": "{target_db}"},
			},
			"marker-only": {
				Markers: []string{"just-a-marker.txt"},
			},
		},
	}
	r := RegistryFor(cfg)
	var got *Spec
	var bare *Spec
	for i := range r.Specs {
		switch r.Specs[i].Name {
		case "sequel":
			got = &r.Specs[i]
		case "marker-only":
			bare = &r.Specs[i]
		}
	}
	if got == nil {
		t.Fatal("custom spec missing from registry")
	}
	if got.MigrateRun != "bundle exec rake db:migrate" {
		t.Errorf("MigrateRun = %q", got.MigrateRun)
	}
	if got.MigrateEnv["DB_NAME"] != "{target_db}" {
		t.Errorf("MigrateEnv = %v", got.MigrateEnv)
	}
	if got.RollbackRun == "" || got.RollbackEnv["DB_NAME"] != "{target_db}" {
		t.Errorf("rollback pair not carried: %q %v", got.RollbackRun, got.RollbackEnv)
	}
	if bare == nil || bare.MigrateRun != "" {
		t.Errorf("custom spec without commands must keep them empty, got %+v", bare)
	}
}
