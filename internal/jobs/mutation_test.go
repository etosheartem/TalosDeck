package jobs

import (
	"context"
	"errors"
	"testing"
)

func TestNestedMutationNeedsCurrentAuthority(t *testing.T) {
	for _, configured := range []bool{false, true} {
		a := &epochAuthority{}
		a.epoch.Store(1)
		m, err := Open(t.TempDir(), func(ctx context.Context, e *Execution, _ Request) error {
			err := e.ValidateExecution(ctx)
			if !configured {
				if !errors.Is(err, ErrUncertain) {
					t.Error("missing authority admitted nested mutation")
				}
				return err
			}
			if err != nil {
				t.Error(err)
				return err
			}
			a.epoch.Store(2)
			if err = e.ValidateExecution(ctx); !errors.Is(err, ErrUncertain) {
				t.Error("superseded executor admitted nested mutation")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if configured {
			if err = m.SetExecutionAuthority(a, "test-executor", 1); err != nil {
				t.Fatal(err)
			}
		}
		j, err := m.Submit(Request{Kind: "test-nested"}, "operator")
		if err != nil {
			t.Fatal(err)
		}
		m.wg.Wait()
		j, err = m.Get(j.ID)
		if err != nil || j.Status != "interrupted" {
			t.Fatalf("unsafe outcome: %s %v", j.Status, err)
		}
		m.Close()
	}
}
