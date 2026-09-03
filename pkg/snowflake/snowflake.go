// Package snowflake implements Twitter's Snowflake distributed ID generator:
// 64-bit signed integers that are roughly time-ordered, unique across a
// cluster, and cheap to produce locally (no coordination once node IDs are
// assigned). Use it for primary keys, order IDs, message IDs — anywhere a
// service needs a sortable unique identifier without a database round-trip
// or a UUID's 128-bit bulk.
//
// # Bit layout
//
//	0 | timestamp (41 bits) | node id (10 bits) | sequence (12 bits)
//	  |  ms since Epoch     |  0..1023          |  0..4095 per ms
//
// The sign bit is always zero, so every ID is a positive int64 and can be
// stored in a signed BIGINT column. With DefaultEpoch (2020-01-01 UTC) the
// 41-bit timestamp field stays valid until ~2089; each node hands out up to
// 4,096,000 IDs per second before Generate blocks until the next millisecond.
//
// # Node IDs
//
// Uniqueness across a cluster depends entirely on every Node getting a
// distinct ID in [0, 1023]. Two nodes with the same ID will collide —
// there is no runtime detection. Choose an assignment strategy:
//
//   - Static config (recommended): each instance reads its ID from a config
//     file or env var; ops owns the allocation. Simplest and safest.
//   - Hostname hash (dev/single-host only): NewNodeFromHostname hashes
//     os.Hostname() into range. Distinct hosts almost always get distinct
//     IDs, but the guarantee is probabilistic — never use in production.
//   - External coordinator (elastic clusters): rent IDs from etcd/zookeeper
//     on startup; out of scope for this package but trivial to plug into
//     NewNode.
//
// # Wiring
//
// Build one Node per process and share it (it is safe for concurrent use):
//
//	node, err := snowflake.NewNode(cfg.SnowflakeNodeId)
//	if err != nil { return err }
//	id, err := node.Generate()
//	if err != nil { return err }
//	user.ID = id.Int64()          // store as BIGINT
//	reply.Id = id.String()        // ship as string to a JS client
//
// # JSON precision
//
// A snowflake easily exceeds 2^53, so a JSON *number* would silently round
// in JavaScript clients. ID implements encoding.TextMarshaler, so it
// serialises as a decimal string in JSON — declare the proto field as
// `string` (or wrap with `int64_as_string` in protojson) to preserve
// precision end-to-end.
package snowflake

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"sync"
	"time"
)

// Bit widths of the three fields packed into an ID. Together with the sign
// bit they add up to 64. Changing them is a hard breaking change: IDs minted
// under the old layout decode wrongly under the new one, so persist the
// choice and never rotate it on a live system.
const (
	nodeBits uint8 = 10
	stepBits uint8 = 12
)

// Derived constants — masks, shifts, and maxima — computed once from the bit
// widths so editing them above stays self-consistent.
const (
	nodeMax   int64 = -1 ^ (-1 << nodeBits) // 1023
	stepMask  int64 = -1 ^ (-1 << stepBits) // 4095
	nodeShift uint8 = stepBits              // 12
	timeShift uint8 = nodeBits + stepBits   // 22
)

// DefaultEpoch is the reference "time zero" for the timestamp field. Using a
// recent epoch (2020-01-01 UTC) instead of the Unix epoch stretches the
// 41-bit range from ~1970..2039 to ~2020..2089. Every Node in one cluster
// must share the same epoch — mixed epochs look like clock drift and break
// ordering.
var DefaultEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrClockBackwards is returned when the wall clock jumps backwards by more
// than driftTolerance. Small drifts are absorbed by waiting; anything larger
// means NTP stepped or the VM migrated, and no local wait can guarantee
// uniqueness.
var ErrClockBackwards = errors.New("snowflake: clock moved backwards")

// driftTolerance bounds how far the wall clock may jump backwards before
// Generate refuses instead of blocking. Within this window we sleep it out;
// past it we return ErrClockBackwards so callers can decide (log, retry,
// fall back) rather than freeze.
const driftTolerance = 5 * time.Millisecond

// ID is a generated snowflake. Its underlying int64 keeps it cheap to store
// and index; call String when it must leave the system as a stable identifier
// (JSON number precision loss above 2^53 makes strings the safe choice for
// browser clients).
type ID int64

// Node hands out unique IDs from a single machine/process. A Node is safe for
// concurrent use; the mutex serialises Generate so the per-millisecond
// sequence stays atomic.
type Node struct {
	mu    sync.Mutex
	epoch int64 // millis, subtracted from now to fit 41 bits
	node  int64 // this node's cluster-unique id
	time  int64 // last-issued millis since epoch
	step  int64 // sequence within time
}

// NewNode builds a Node with the given ID and DefaultEpoch. IDs must be in
// [0, nodeMax]; the returned error surfaces out-of-range values instead of
// silently masking them, since two nodes with the same effective ID would
// hand out colliding snowflakes.
func NewNode(id int64) (*Node, error) {
	return NewNodeWithEpoch(id, DefaultEpoch)
}

// NewNodeWithEpoch builds a Node pinned to a custom epoch. All timestamps in
// generated IDs are measured from this instant, so every Node in one cluster
// must share it — mixed epochs look like clock drift.
func NewNodeWithEpoch(id int64, epoch time.Time) (*Node, error) {
	if id < 0 || id > nodeMax {
		return nil, fmt.Errorf("snowflake: node id %d out of range [0,%d]", id, nodeMax)
	}
	if epoch.IsZero() {
		return nil, errors.New("snowflake: epoch must not be zero")
	}
	if epoch.UnixMilli() > time.Now().UnixMilli() {
		return nil, errors.New("snowflake: epoch must not be in the future")
	}
	return &Node{
		epoch: epoch.UnixMilli(),
		node:  id,
	}, nil
}

// MustNewNode is NewNode but panics on error. Convenient for package-level
// vars where the caller knows the ID is in range.
func MustNewNode(id int64) *Node {
	n, err := NewNode(id)
	if err != nil {
		panic(err)
	}
	return n
}

// NewNodeFromHostname derives a node ID by hashing os.Hostname() into range.
// Distinct hosts almost always get distinct IDs, but this is a probabilistic
// convenience for local dev or a small static cluster — production
// deployments should assign IDs explicitly through config or an external
// coordinator so two hosts never collide.
func NewNodeFromHostname() (*Node, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("snowflake: hostname: %w", err)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(host))
	return NewNode(int64(h.Sum32() & uint32(nodeMax)))
}

// Generate returns the next unique ID. It blocks only when the current
// millisecond's 4096-step sequence is exhausted, or when the clock drifts
// backwards by up to driftTolerance; a larger drift returns
// ErrClockBackwards instead of risking duplicates.
//
// The zero ID is never returned: a real ID always carries a positive
// timestamp offset.
func (n *Node) Generate() (ID, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := nowMillis() - n.epoch

	if now < n.time {
		// Clock moved backwards. Refuse a large drift outright — no local wait
		// can guarantee uniqueness after an NTP step or a VM migration. Spin
		// out a small one; the bound keeps the wait finite.
		drift := time.Duration(n.time-now) * time.Millisecond
		if drift > driftTolerance {
			return 0, fmt.Errorf("%w: drift=%s (last=%d now=%d)", ErrClockBackwards, drift, n.time, now)
		}
		for now < n.time {
			now = nowMillis() - n.epoch
		}
	}

	if now == n.time {
		n.step = (n.step + 1) & stepMask
		if n.step == 0 {
			// Sequence exhausted for this ms. Spin — still under the lock —
			// until the wall clock ticks over. Releasing here would let
			// another goroutine re-issue a step from the same ms.
			for now <= n.time {
				now = nowMillis() - n.epoch
			}
		}
	} else {
		// New ms — reset the sequence.
		n.step = 0
	}

	n.time = now
	return ID((now << timeShift) | (n.node << nodeShift) | n.step), nil
}

// MustGenerate is Generate but panics on clock drift. Prefer Generate in
// library code; MustGenerate is convenient for one-off scripts and tests
// where handling the error path adds noise.
func (n *Node) MustGenerate() ID {
	id, err := n.Generate()
	if err != nil {
		panic(err)
	}
	return id
}

// nowMillis returns the current wall-clock time in Unix milliseconds. Kept
// as a var so tests can stub it and drive the clock manually.
var nowMillis = func() int64 {
	return time.Now().UnixMilli()
}

// Time returns the ID's timestamp as a time.Time in the local zone. The
// value assumes DefaultEpoch; IDs minted with a custom epoch must be decoded
// via TimeWithEpoch.
func (f ID) Time() time.Time {
	return f.TimeWithEpoch(DefaultEpoch)
}

// TimeWithEpoch returns the ID's timestamp decoded against the given epoch.
func (f ID) TimeWithEpoch(epoch time.Time) time.Time {
	return time.UnixMilli((int64(f) >> timeShift) + epoch.UnixMilli())
}

// Node returns the node ID that produced this ID.
func (f ID) Node() int64 {
	return (int64(f) >> nodeShift) & nodeMax
}

// Step returns the per-millisecond sequence value of this ID.
func (f ID) Step() int64 {
	return int64(f) & stepMask
}

// Int64 returns the raw underlying value — useful for storing in a database
// column typed BIGINT.
func (f ID) Int64() int64 {
	return int64(f)
}

// String returns the ID as its base-10 digits. Prefer this (or a
// string-typed proto field) when shipping IDs to a JavaScript client: a
// 64-bit snowflake can exceed Number.MAX_SAFE_INTEGER (2^53), and a JSON
// number would silently round.
func (f ID) String() string {
	return strconv.FormatInt(int64(f), 10)
}

// MarshalText encodes the ID as its decimal string. Combined with
// UnmarshalText this makes IDs round-trip safely through JSON without
// losing precision on JavaScript clients.
func (f ID) MarshalText() ([]byte, error) {
	return []byte(f.String()), nil
}

// UnmarshalText parses a decimal-encoded ID.
func (f *ID) UnmarshalText(text []byte) error {
	v, err := strconv.ParseInt(string(text), 10, 64)
	if err != nil {
		return fmt.Errorf("snowflake: parse id %q: %w", text, err)
	}
	*f = ID(v)
	return nil
}
