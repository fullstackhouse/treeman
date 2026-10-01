package prepare

import (
	"slices"
	"testing"
	"time"
)

// TestESDurablePrefix pins the orphan-reconcile classifier: it must recognise a
// treeman ES branch durable ("tmbs_<16hex>_…") and its family prefix, and must
// reject every other ES index family the reconcile must never touch — snapshot
// cache (`tm_`), active clones (`kho_`), and base data (`client_*`/`dev_*`). A
// false positive here would drop live or cached data.
func TestESDurablePrefix(t *testing.T) {
	cases := []struct {
		name       string
		index      string
		wantPrefix string
		wantOK     bool
	}{
		{"durable with appended index", "tmbs_eef6294a51701034__client_48_category_232_pim_end", "tmbs_eef6294a51701034_", true},
		{"durable bare prefix", "tmbs_1a7984f692fa4bab_", "tmbs_1a7984f692fa4bab_", true},
		{"snapshot cache template rejected", "tm_eef6294a51701034_client_48", "", false},
		{"active clone rejected", "kho_kon_12660_client_44_category_210_pim_end", "", false},
		{"base files alias rejected", "client_411_files_alias", "", false},
		{"base dev index rejected", "dev_client_48_category_232_pim_tree", "", false},
		{"hash too short rejected", "tmbs_abc_client", "", false},
		{"non-hex hash rejected", "tmbs_zzzzzzzzzzzzzzzz_client", "", false},
		{"missing trailing underscore rejected", "tmbs_eef6294a51701034client", "", false},
		{"empty rejected", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := esDurablePrefix(tc.index)
			if ok != tc.wantOK || got != tc.wantPrefix {
				t.Fatalf("esDurablePrefix(%q) = (%q, %t), want (%q, %t)",
					tc.index, got, ok, tc.wantPrefix, tc.wantOK)
			}
		})
	}
}

// TestDurableFamily pins the untracked-durable classifier: only names that
// are EXACTLY a durable spelling for the engine are candidates. A user
// database that merely starts with the marker, a snapshot-cache template, or
// an active namespace must never be classified as a durable.
func TestDurableFamily(t *testing.T) {
	cases := []struct {
		eng, name, want string
		ok              bool
	}{
		{"mysql", "_tmbs_472a70bcdcaf376e", "_tmbs_472a70bcdcaf376e", true},
		{"postgres", "_tmbs_472a70bcdcaf376e", "_tmbs_472a70bcdcaf376e", true},
		{"mongodb", "_tmbs_42a1486876c3f122", "_tmbs_42a1486876c3f122", true},
		{"s3", "tmbs-a5c39fdc93ed9974", "tmbs-a5c39fdc93ed9974", true},
		{"elasticsearch", "tmbs_d5f5a4d6a4833f19__client_1", "tmbs_d5f5a4d6a4833f19_", true},
		{"mysql", "_tmbs_472a70bcdcaf376e_extra", "", false},
		{"mysql", "_tmbs_472a70bcdcaf376", "", false},
		{"mysql", "_tmbs_472A70BCDCAF376E", "", false},
		{"mysql", "_tm_472a70bcdcaf376e", "", false},
		{"mysql", "kontainer", "", false},
		{"s3", "tmbs-a5c39fdc93ed9974-media", "", false},
		{"s3", "kontainer-dev-admin", "", false},
		{"redis", "_tmbs:472a70bcdcaf376e:", "", false},
	}
	for _, tc := range cases {
		got, ok := durableFamily(tc.eng, tc.name)
		if ok != tc.ok || got != tc.want {
			t.Errorf("durableFamily(%q, %q) = (%q, %t), want (%q, %t)", tc.eng, tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestUntrackedSeenGrace pins the race guard: an untracked durable is only
// due once it has stayed untracked for the grace window across sweeps, and
// a durable that becomes tracked (or disappears) restarts its clock.
func TestUntrackedSeenGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	u := &UntrackedSeen{Grace: 10 * time.Minute, Now: func() time.Time { return now }}
	set := func(names ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, n := range names {
			m[n] = struct{}{}
		}
		return m
	}

	if due := u.observe("mysql", set("a", "b")); len(due) != 0 {
		t.Fatalf("first sighting must never be due, got %v", due)
	}
	now = now.Add(5 * time.Minute)
	if due := u.observe("mysql", set("a", "b")); len(due) != 0 {
		t.Fatalf("inside grace must not be due, got %v", due)
	}
	// "b" got tracked in between → forgotten.
	now = now.Add(5 * time.Minute)
	if due := u.observe("mysql", set("a")); !slices.Equal(due, []string{"a"}) {
		t.Fatalf("due = %v, want [a]", due)
	}
	// "b" untracked again restarts its clock.
	if due := u.observe("mysql", set("a", "b")); !slices.Equal(due, []string{"a"}) {
		t.Fatalf("due = %v, want [a] (b's clock restarted)", due)
	}
	// Engines are independent: the same name on another engine is new.
	if due := u.observe("mongodb", set("a")); len(due) != 0 {
		t.Fatalf("other engine's first sighting must not be due, got %v", due)
	}
}
