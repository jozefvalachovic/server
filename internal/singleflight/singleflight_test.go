package singleflight

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_Basic(t *testing.T) {
	var g Group
	v, err, shared := g.Do("k", func() (any, error) { return 42, nil })
	if err != nil {
		t.Fatal(err)
	}
	if v.(int) != 42 {
		t.Fatalf("want 42, got %v", v)
	}
	if shared {
		t.Fatal("single caller should not be shared")
	}
}

func TestDo_Error(t *testing.T) {
	var g Group
	want := errors.New("boom")
	_, err, _ := g.Do("k", func() (any, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
}

func TestDo_Deduplicates(t *testing.T) {
	var g Group
	var calls atomic.Int32
	start := make(chan struct{})

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)

	results := make([]any, n)
	sharedFlags := make([]bool, n)

	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			v, _, shared := g.Do("same-key", func() (any, error) {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return "result", nil
			})
			results[i] = v
			sharedFlags[i] = shared
		}(i)
	}

	close(start)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("fn ran %d times; want exactly 1", got)
	}
	for i, v := range results {
		if v != "result" {
			t.Fatalf("caller %d got %v; want 'result'", i, v)
		}
	}

	// Every caller (leader included) sees shared=true when the value was
	// shared with at least one follower — matching x/sync/singleflight.
	for i, s := range sharedFlags {
		if !s {
			t.Fatalf("caller %d: shared=false; want true (value was shared)", i)
		}
	}
}

func TestDo_AllowsSubsequentCalls(t *testing.T) {
	var g Group
	var calls atomic.Int32
	fn := func() (any, error) {
		calls.Add(1)
		return calls.Load(), nil
	}

	for range 5 {
		if _, err, _ := g.Do("k", fn); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("want 5 sequential calls; got %d", got)
	}
}
