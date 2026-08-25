package bus

// The in-process implementation of the port (P1.2).
//
// This is the half of P1.1 that pays for it: with this, a deployment that does
// not want Kafka does not need it, and two of the twenty-three containers stop
// being mandatory. It is not a lightweight substitute — it writes every record
// to a durable, checksummed, crash-recoverable log before Publish returns,
// because a lite deployment that loses its queue on restart is a data-loss bug
// shipped on purpose.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/pulsetrace/shared/bus/wal"
	"github.com/pulsetrace/shared/models"
)

// topicNamePattern is what a topic may be called; it becomes a directory name.
var topicNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// InProcessBus is the single-binary transport, backed by a per-topic WAL.
type InProcessBus struct {
	mu   sync.Mutex
	dir  string
	opts wal.Options

	logs   map[string]*wal.Log
	groups map[string]*wal.Group // keyed topic+"/"+group

	// startEarliest makes a never-committed group replay from the oldest
	// retained record instead of starting at the end.
	startEarliest bool

	closed bool
}

// InProcessOptions configure the bus. The zero value is valid.
type InProcessOptions struct {
	// SegmentBytes, SyncInterval, MaxBytes and FullTimeout are passed to each
	// topic's log. See wal.Options.
	SegmentBytes int64
	SyncInterval time.Duration
	MaxBytes     int64
	FullTimeout  time.Duration
	// StartFromEarliest makes a group with no committed offset replay from the
	// oldest retained record. Off by default, because the Kafka path this
	// replaces starts a new group at the end and the two must agree.
	StartFromEarliest bool
}

// NewInProcessBus opens (or creates) the bus rooted at dir.
func NewInProcessBus(dir string, o InProcessOptions) (*InProcessBus, error) {
	if dir == "" {
		return nil, errors.New("bus: in-process bus requires a data directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("bus: create %s: %w", dir, err)
	}
	return &InProcessBus{
		dir: dir,
		opts: wal.Options{
			SegmentBytes: o.SegmentBytes,
			SyncInterval: o.SyncInterval,
			MaxBytes:     o.MaxBytes,
			FullTimeout:  o.FullTimeout,
		},
		startEarliest: o.StartFromEarliest,
		logs:          map[string]*wal.Log{},
		groups:        map[string]*wal.Group{},
	}, nil
}

// topicLog returns the log for a topic, opening it on first use.
func (b *InProcessBus) topicLog(topic string) (*wal.Log, error) {
	if !topicNamePattern.MatchString(topic) {
		return nil, fmt.Errorf("bus: refusing topic name %q: outside the permitted character set", topic)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("bus: closed")
	}
	if l, ok := b.logs[topic]; ok {
		return l, nil
	}
	l, err := wal.Open(filepath.Join(b.dir, topic), b.opts)
	if err != nil {
		return nil, err
	}
	b.logs[topic] = l
	return l, nil
}

func (b *InProcessBus) Publish(ctx context.Context, topic, key string, value []byte) error {
	l, err := b.topicLog(topic)
	if err != nil {
		return err
	}
	rec := wal.Record{
		Timestamp: time.Now().UTC(),
		Key:       key,
		Value:     value,
		// Trace context travels in headers, exactly as it does over Kafka, so a
		// consumer cannot tell which transport carried it.
		Headers: headersFromContext(ctx),
	}
	if _, err := l.AppendWithBackpressure(rec); err != nil {
		if errors.Is(err, wal.ErrLogFull) {
			// Surfaced as ErrBusFull so a caller maps it to 429 without knowing
			// which implementation refused.
			return fmt.Errorf("%w: %s", ErrBusFull, err)
		}
		return err
	}
	return nil
}

func (b *InProcessBus) PublishBatch(ctx context.Context, topic string, entries []*models.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	l, err := b.topicLog(topic)
	if err != nil {
		return err
	}
	headers := headersFromContext(ctx)
	for _, e := range entries {
		payload, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("bus: marshal entry: %w", err)
		}
		rec := wal.Record{
			Timestamp: time.Now().UTC(),
			Key:       e.ServiceName,
			Value:     payload,
			Headers:   headers,
		}
		if _, err := l.AppendWithBackpressure(rec); err != nil {
			if errors.Is(err, wal.ErrLogFull) {
				return fmt.Errorf("%w: %s", ErrBusFull, err)
			}
			return err
		}
	}
	return nil
}

func (b *InProcessBus) Subscribe(group string, topics []string, h Handler) (Subscription, error) {
	if h == nil {
		return nil, errors.New("bus: Subscribe requires a handler")
	}
	if len(topics) == 0 {
		return nil, errors.New("bus: Subscribe requires at least one topic")
	}
	sub := &inProcessSubscription{handler: h}
	for _, topic := range topics {
		l, err := b.topicLog(topic)
		if err != nil {
			return nil, err
		}
		g, err := b.groupFor(topic, group)
		if err != nil {
			return nil, err
		}
		// Where a brand-new group starts.
		//
		// The conformance suite caught this on its first run against both
		// transports: Kafka is configured `OffsetNewest`, so a group with no
		// committed offset begins at the *end* and never sees what was published
		// before it joined. Reading from zero — the obvious choice for a durable
		// log, and what this did first — is therefore a different product, not a
		// drop-in replacement, and the difference would have surfaced as a lite
		// deployment replaying its entire retention window on first boot.
		//
		// Matching the incumbent is the whole point of a strangler port, so a
		// never-committed group is seeded at the current end. StartFromEarliest
		// opts into replay for callers who genuinely want it.
		if !g.HasCommitted() {
			start := l.NextOffset()
			if b.startEarliest {
				start = l.OldestOffset()
			}
			if err := g.StartAt(start); err != nil {
				return nil, err
			}
		}
		sub.readers = append(sub.readers, topicReader{
			topic:  topic,
			log:    l,
			group:  g,
			reader: wal.NewReader(l, g),
		})
	}
	return sub, nil
}

func (b *InProcessBus) groupFor(topic, group string) (*wal.Group, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := topic + "/" + group
	if g, ok := b.groups[key]; ok {
		return g, nil
	}
	g, err := wal.OpenGroup(filepath.Join(b.dir, topic), group)
	if err != nil {
		return nil, err
	}
	b.groups[key] = g
	return g, nil
}

func (b *InProcessBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var firstErr error
	for _, l := range b.logs {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type topicReader struct {
	topic  string
	log    *wal.Log
	group  *wal.Group
	reader *wal.Reader
}

type inProcessSubscription struct {
	readers []topicReader
	handler Handler
}

// Run consumes every subscribed topic concurrently until ctx is cancelled.
//
// One goroutine per topic rather than a merged stream: per-topic ordering is
// the guarantee the Kafka path makes, and interleaving topics through a single
// loop would let a busy topic starve a quiet one.
func (s *inProcessSubscription) Run(ctx context.Context) error {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, tr := range s.readers {
		wg.Add(1)
		go func(tr topicReader) {
			defer wg.Done()
			err := tr.reader.Run(ctx, func(ctx context.Context, offset int64, rec wal.Record) error {
				m := Message{
					Topic:     tr.topic,
					Key:       rec.Key,
					Value:     rec.Value,
					Timestamp: rec.Timestamp,
					Offset:    offset,
					Headers:   rec.Headers,
				}
				// Same contract as the Kafka adapter: the handler's ctx already
				// continues the producer's trace, and a handler error is logged
				// by the caller rather than stopping the subscription.
				if err := s.handler(contextWithTrace(ctx, m.Headers), m); err != nil {
					// Matching Kafka's poison-pill behaviour deliberately: the
					// record is marked processed so one unreadable message
					// cannot wedge the topic forever. Changing that is a
					// separate decision from choosing a transport.
					return nil
				}
				return nil
			})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(tr)
	}
	wg.Wait()
	return firstErr
}

func (s *inProcessSubscription) Close() error { return nil }

// headersFromContext extracts the outgoing trace context as bus headers.
func headersFromContext(ctx context.Context) map[string][]byte {
	carrier := traceCarrier{}
	injectTrace(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(carrier))
	for k, v := range carrier {
		out[k] = []byte(v)
	}
	return out
}

var (
	_ Bus          = (*InProcessBus)(nil)
	_ Subscription = (*inProcessSubscription)(nil)
)
