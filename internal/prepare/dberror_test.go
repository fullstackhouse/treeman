package prepare

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// TestFailedDBIndices: per-database failures stay attributable through
// %w wrapping, so finalize recovery can drop only the
// databases that failed (#119).
func TestFailedDBIndices(t *testing.T) {
	cause := errors.New("migrate failed")
	joined := &RunError{Failed: []*DBError{
		{Index: 0, Engine: "mysql", Err: cause},
		{Index: 2, Engine: "redis", Err: errors.New("dial")},
	}}
	if !errors.Is(fmt.Errorf("prepare: %w", joined), cause) {
		t.Fatal("RunError must unwrap to each database's cause")
	}
	if got := FailedDBIndices(fmt.Errorf("prepare: %w", joined)); !slices.Equal(got, []int{0, 2}) {
		t.Fatalf("FailedDBIndices = %v, want [0 2]", got)
	}
	if got := FailedDBIndices(errors.New("create-after-engines hook failed")); got != nil {
		t.Fatalf("unattributed error: got %v, want nil", got)
	}
	if got := FailedDBIndices(nil); got != nil {
		t.Fatalf("nil error: got %v, want nil", got)
	}
}
