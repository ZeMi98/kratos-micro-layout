package snowflake

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestNewNode covers the range check and epoch validation. Every branch is a
// real failure mode callers hit when the node ID comes from config.
func TestNewNode(t *testing.T) {
	if _, err := NewNode(0); err != nil {
		t.Errorf("NewNode(0): unexpected err %v", err)
	}
	if _, err := NewNode(nodeMax); err != nil {
		t.Errorf("NewNode(%d): unexpected err %v", nodeMax, err)
	}
	if _, err := NewNode(-1); err == nil {
		t.Error("NewNode(-1): expected range error")
	}
	if _, err := NewNode(nodeMax + 1); err == nil {
		t.Error("NewNode(nodeMax+1): expected range error")
	}
	if _, err := NewNodeWithEpoch(1, time.Time{}); err == nil {
		t.Error("NewNodeWithEpoch(zero): expected error")
	}
	if _, err := NewNodeWithEpoch(1, time.Now().Add(time.Hour)); err == nil {
		t.Error("NewNodeWithEpoch(future): expected error")
	}
}

// TestGenerate_UniqueAndMonotonic exercises the happy path: sequential
// Generate calls within one node must never collide and must not go
// backwards. The loop count is high enough to force the per-ms sequence to
// wrap at least once on a normal machine.
func TestGenerate_UniqueAndMonotonic(t *testing.T) {
	n := MustNewNode(1)
	const count = 10_000
	seen := make(map[ID]struct{}, count)
	var prev ID
	for i := 0; i < count; i++ {
		id, err := n.Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if id <= 0 {
			t.Fatalf("Generate returned non-positive id %d", id)
		}
		if id <= prev {
			t.Fatalf("id %d did not increase over prev %d", id, prev)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %d at iteration %d", id, i)
		}
		seen[id] = struct{}{}
		prev = id
	}
}

// TestGenerate_Concurrent is the real uniqueness test: many goroutines
// hammering one Node must never produce a duplicate. Uniqueness under
// concurrency is the whole point of the mutex + sequence design.
func TestGenerate_Concurrent(t *testing.T) {
	n := MustNewNode(7)
	const (
		goroutines = 32
		perRoutine = 2_000
	)
	ids := make(chan ID, goroutines*perRoutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				id, err := n.Generate()
				if err != nil {
					t.Errorf("Generate: %v", err)
					return
				}
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[ID]struct{}, goroutines*perRoutine)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %d under concurrency", id)
		}
		seen[id] = struct{}{}
	}
	if got, want := len(seen), goroutines*perRoutine; got != want {
		t.Fatalf("collected %d ids, want %d", got, want)
	}
}

// TestGenerate_DistinctNodesDontCollide simulates a two-node cluster running
// in the same process. Same-ms IDs must differ thanks to the node field.
func TestGenerate_DistinctNodesDontCollide(t *testing.T) {
	a := MustNewNode(1)
	b := MustNewNode(2)
	seen := make(map[ID]struct{}, 2000)
	for i := 0; i < 1000; i++ {
		ia, err := a.Generate()
		if err != nil {
			t.Fatalf("a.Generate: %v", err)
		}
		ib, err := b.Generate()
		if err != nil {
			t.Fatalf("b.Generate: %v", err)
		}
		if ia == ib {
			t.Fatalf("node a and b produced the same id %d", ia)
		}
		if ia.Node() != 1 || ib.Node() != 2 {
			t.Fatalf("node field mis-decoded: a=%d b=%d", ia.Node(), ib.Node())
		}
		seen[ia] = struct{}{}
		seen[ib] = struct{}{}
	}
	if len(seen) != 2000 {
		t.Fatalf("expected 2000 unique ids, got %d", len(seen))
	}
}

// TestID_Fields decodes a hand-built ID and checks every accessor. The bit
// layout is the package's public contract — a regression here silently
// breaks every consumer that stores or transmits these IDs.
func TestID_Fields(t *testing.T) {
	const (
		wantTime int64 = 12345
		wantNode int64 = 42
		wantStep int64 = 7
	)
	raw := (wantTime << timeShift) | (wantNode << nodeShift) | wantStep
	id := ID(raw)
	if got := id.Node(); got != wantNode {
		t.Errorf("Node() = %d, want %d", got, wantNode)
	}
	if got := id.Step(); got != wantStep {
		t.Errorf("Step() = %d, want %d", got, wantStep)
	}
	wantMillis := wantTime + DefaultEpoch.UnixMilli()
	if got := id.Time().UnixMilli(); got != wantMillis {
		t.Errorf("Time().UnixMilli() = %d, want %d", got, wantMillis)
	}
	if got := id.Int64(); got != raw {
		t.Errorf("Int64() = %d, want %d", got, raw)
	}
}

// TestID_TextRoundTrip covers encoding.TextMarshaler — the recommended way
// to ship IDs to a JSON consumer without losing precision above 2^53.
func TestID_TextRoundTrip(t *testing.T) {
	orig := ID(1) << 62 // deliberately above 2^53
	data, err := json.Marshal(struct {
		ID ID `json:"id"`
	}{ID: orig})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The encoded form must be a JSON string, not a number — that's the whole
	// point of implementing TextMarshaler.
	if got, want := string(data), `{"id":"`+orig.String()+`"}`; got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
	var back struct {
		ID ID `json:"id"`
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != orig {
		t.Fatalf("round-trip = %d, want %d", back.ID, orig)
	}
}

// TestNewNodeFromHostname just checks the helper produces a valid node —
// the exact ID depends on the host and cannot be asserted portably.
func TestNewNodeFromHostname(t *testing.T) {
	n, err := NewNodeFromHostname()
	if err != nil {
		t.Fatalf("NewNodeFromHostname: %v", err)
	}
	if _, err := n.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

// TestGenerate_ClockBackwards stubs nowMillis to simulate a wall-clock jump.
// A small drift must be absorbed by waiting; a large drift must surface
// ErrClockBackwards rather than mint a duplicate.
func TestGenerate_ClockBackwards(t *testing.T) {
	// Small drift (within tolerance): Generate waits it out and still
	// returns a valid, strictly-increasing ID.
	t.Run("small drift waits", func(t *testing.T) {
		n := MustNewNode(3)
		first, err := n.Generate()
		if err != nil {
			t.Fatalf("first Generate: %v", err)
		}
		// Freeze the clock 2ms in the past (below driftTolerance=5ms) for a
		// single reading, then let it recover.
		realNow := nowMillis
		var calls int
		nowMillis = func() int64 {
			calls++
			if calls == 1 {
				return realNow() - 2
			}
			return realNow()
		}
		defer func() { nowMillis = realNow }()

		second, err := n.Generate()
		if err != nil {
			t.Fatalf("second Generate: %v", err)
		}
		if second <= first {
			t.Fatalf("second id %d did not increase over first %d", second, first)
		}
	})

	t.Run("large drift errors", func(t *testing.T) {
		n := MustNewNode(4)
		if _, err := n.Generate(); err != nil {
			t.Fatalf("seed Generate: %v", err)
		}
		realNow := nowMillis
		nowMillis = func() int64 { return realNow() - int64(time.Second/time.Millisecond) }
		defer func() { nowMillis = realNow }()

		_, err := n.Generate()
		if !errors.Is(err, ErrClockBackwards) {
			t.Fatalf("expected ErrClockBackwards, got %v", err)
		}
	})
}

// TestGenerate_SequenceExhaustion forces the per-ms counter to wrap and
// verifies the next ID lands in the following millisecond rather than
// re-using step 0 of the current one.
func TestGenerate_SequenceExhaustion(t *testing.T) {
	n := MustNewNode(5)
	// Freeze the clock at a fixed ms so every call hits the same-ms branch.
	frozen := time.Now().UnixMilli()
	realNow := nowMillis
	var unfreeze bool
	nowMillis = func() int64 {
		if unfreeze {
			return frozen + 1
		}
		return frozen
	}
	defer func() { nowMillis = realNow }()

	// First call: now > n.time (n.time is 0), so step resets to 0 and issues.
	first, err := n.Generate()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Step() != 0 {
		t.Fatalf("first step = %d, want 0", first.Step())
	}
	// Burn through steps 1..stepMask (4095) — all in the same ms.
	for i := int64(1); i <= stepMask; i++ {
		id, err := n.Generate()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if id.Step() != i {
			t.Fatalf("iter %d: step = %d", i, id.Step())
		}
	}
	// Next call: step would wrap to 0, so Generate must advance to the next
	// ms and reset the sequence.
	unfreeze = true
	next, err := n.Generate()
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if next.Step() != 0 {
		t.Fatalf("wrap step = %d, want 0", next.Step())
	}
	if next.Time().UnixMilli() != frozen+1 {
		t.Fatalf("wrap time = %d, want %d", next.Time().UnixMilli(), frozen+1)
	}
}
