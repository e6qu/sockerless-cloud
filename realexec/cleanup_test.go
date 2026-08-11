package realexec

import (
	"context"
	"reflect"
	"testing"
)

func TestCleanupStackRunsReverseOrder(t *testing.T) {
	var stack CleanupStack
	var got []int
	stack.Add(func(context.Context) error {
		got = append(got, 1)
		return nil
	})
	stack.Add(func(context.Context) error {
		got = append(got, 2)
		return nil
	})
	if err := stack.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []int{2, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup order = %v, want %v", got, want)
	}
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("second close should be idempotent: %v", err)
	}
}
