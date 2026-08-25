package wal

import (
	"context"
	"fmt"
	"time"
)

// pollInterval is how long a caught-up reader waits before looking again.
//
// A condition variable would wake instantly, but it also has to be woken from
// the append path, which means the writer takes the reader's lock on every
// record. At this interval a caught-up consumer costs one index read per tick
// and an idle topic costs nothing measurable, while the latency it adds is
// invisible next to the fsync window records are already waiting through.
const pollInterval = 20 * time.Millisecond

// Deliver is called for each record, with the offset it sits at.
//
// Returning an error stops the reader *without committing that record*, so it
// will be redelivered. That is the correct response to "I could not process
// this": the alternative is to advance past a record nobody handled, which is a
// silent drop.
type Deliver func(ctx context.Context, offset int64, r Record) error

// Reader delivers a topic's records to one consumer group, in order, starting
// from the group's committed position.
//
// # At-least-once, and where the decision is
//
// The offset is committed *after* the handler returns, one record at a time.
// A crash between handling and committing redelivers the record on restart —
// which is at-least-once, is what the Kafka path already does, and is the only
// choice available without a transaction spanning the handler's side effects.
//
// Committing before the handler would be at-most-once: faster, and it loses
// records on exactly the failure this whole package exists to survive.
type Reader struct {
	log   *Log
	group *Group
}

func NewReader(l *Log, g *Group) *Reader { return &Reader{log: l, group: g} }

// Run delivers records until ctx is cancelled.
//
// A cancelled context is a clean stop, not an error: it is how a service shuts
// down. Whatever was committed stays committed, and the next Run resumes there.
func (r *Reader) Run(ctx context.Context, deliver Deliver) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		from := r.group.Position()
		batch, err := r.log.ReadFrom(from)
		if err != nil {
			return fmt.Errorf("wal: read for group %q: %w", r.group.Name(), err)
		}

		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pollInterval):
			}
			continue
		}

		for i, rec := range batch {
			if ctx.Err() != nil {
				// Stop before handling, not midway: a record handed to the
				// handler during shutdown that then failed to commit would be
				// redelivered anyway, so not starting it is cheaper and no less
				// correct.
				return nil
			}
			offset := from + int64(i)
			if err := deliver(ctx, offset, rec); err != nil {
				// Deliberately not committed. The record is redelivered next
				// time, which is what at-least-once means.
				return fmt.Errorf("wal: group %q handler failed at offset %d: %w",
					r.group.Name(), offset, err)
			}
			if err := r.group.Commit(offset + 1); err != nil {
				return fmt.Errorf("wal: group %q commit at offset %d: %w",
					r.group.Name(), offset, err)
			}
		}
	}
}
