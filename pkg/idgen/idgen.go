package idgen

import (
	"kratos-micro-layout/pkg/snowflake"
)

// Generator mints cluster-unique int64 identifiers. It is the service-agnostic
// seam a biz layer depends on: assigning the ID in biz (before the object
// reaches the repo) keeps every ORM implementation persisting an
// application-chosen key rather than a database default, so the ent and gorm
// paths cannot diverge. Depending on this interface — not on a concrete
// snowflake node — also lets tests inject a deterministic generator.
type Generator interface {
	// NextID returns the next unique identifier.
	NextID() (int64, error)
}

// snowflakeGenerator adapts pkg/snowflake to Generator. A single *snowflake.Node
// is safe for concurrent use, so one instance is shared across all requests.
type snowflakeGenerator struct {
	node *snowflake.Node
}

// NewSnowflake builds a Generator backed by a snowflake node with the given
// cluster node ID (must be in [0, 1023]). Two nodes sharing an ID would hand out
// colliding identifiers, so each instance in a cluster needs a distinct one.
func NewSnowflake(nodeID int64) (Generator, error) {
	node, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, err
	}
	return &snowflakeGenerator{node: node}, nil
}

// NextID returns the next snowflake as a raw int64 for storage in a BIGINT key.
func (g *snowflakeGenerator) NextID() (int64, error) {
	id, err := g.node.Generate()
	if err != nil {
		return 0, err
	}
	return id.Int64(), nil
}
