package app

import (
	"context"

	"github.com/lingengyuan/skillctl/internal/fsutil"
)

// observation owns memoized reads for one immutable local inspection snapshot.
// Execution must create a new observation or use the transaction fingerprints;
// these values never replace the checks immediately before a write.
type observation struct {
	ctx    context.Context
	hashes map[string]observedHash
}

type observedHash struct {
	digest string
	err    error
}

func newObservation(ctx context.Context) *observation {
	return &observation{ctx: ctx, hashes: map[string]observedHash{}}
}

func (o *observation) hash(path string) (string, error) {
	if err := o.ctx.Err(); err != nil {
		return "", err
	}
	key := fsutil.PathKey(fsutil.PhysicalPath(path))
	if value, ok := o.hashes[key]; ok {
		return value.digest, value.err
	}
	digest, err := fsutil.HashDirectoryContext(o.ctx, path)
	o.hashes[key] = observedHash{digest: digest, err: err}
	return digest, err
}
