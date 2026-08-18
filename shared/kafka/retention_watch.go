package kafka

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/IBM/sarama"
)

// Retention watchdog.
//
// Kafka here is a transport buffer, not the system of record — Quickwit is.
// Retention was 168h, which meant a full second copy of every log record for a
// week and, measured against a 2 GiB ingest, 4.32 GiB of Kafka: the single
// largest line in our storage footprint. It is now 24h.
//
// That trade is only safe if somebody notices when a consumer falls behind.
// With a week of slack, lag was an efficiency question. With a day, a consumer
// that stalls overnight loses records permanently and silently — the broker
// deletes a segment, the consumer resumes at whatever is left, and nothing in
// the system says a gap exists. `logs` has three independent consumer groups
// (Quickwit, alert-service, topology-service); any one of them can fall behind
// alone.
//
// So this watches the thing that actually matters, which is not lag. Lag says
// how far behind a consumer is; it says nothing about whether the records it
// has not read still exist. The signal is the consumer's committed offset
// against the *oldest offset the broker still holds*:
//
//	committed < oldest  →  records were deleted before this group read them.
//	                       Data loss has already happened. Not a warning.
//	committed - oldest  →  headroom: how much runway is left before it does.
//
// Headroom is reported as a fraction of the partition's retained span, because
// "12,000 records of headroom" means something different on a partition holding
// 20,000 than on one holding 20 million.

// RetentionState classifies one partition's position relative to what the
// broker still holds.
type RetentionState string

const (
	// RetentionOK — the consumer's position is comfortably inside the window.
	RetentionOK RetentionState = "ok"
	// RetentionAtRisk — the consumer is still inside the window, but close
	// enough to its trailing edge that continued slippage loses data.
	RetentionAtRisk RetentionState = "at_risk"
	// RetentionDataLost — the broker has already deleted records this group
	// never read. Unrecoverable; the only question left is how much.
	RetentionDataLost RetentionState = "data_lost"
	// RetentionUnknown — the group has never committed an offset for this
	// partition, so there is no position to judge. A brand-new consumer looks
	// like this, and so does one that has never successfully run.
	RetentionUnknown RetentionState = "unknown"
)

// DefaultRiskFraction is the share of a partition's retained span below which
// remaining headroom is treated as at-risk. A consumer sitting in the oldest
// 10% of what the broker still holds is one bad hour from losing data.
const DefaultRiskFraction = 0.10

// PartitionRetention is one consumer group's standing on one partition.
type PartitionRetention struct {
	Group     string         `json:"group"`
	Topic     string         `json:"topic"`
	Partition int32          `json:"partition"`
	Oldest    int64          `json:"oldest"`    // earliest offset the broker still holds
	Newest    int64          `json:"newest"`    // high water mark
	Committed int64          `json:"committed"` // -1 when the group has never committed
	Lag       int64          `json:"lag"`       // records produced but not yet consumed
	Headroom  int64          `json:"headroom"`  // records between the trailing edge and this consumer
	Lost      int64          `json:"lost"`      // records deleted before this group read them
	State     RetentionState `json:"state"`
}

// classifyRetention is the whole decision, kept free of Sarama so it can be
// tested at its boundaries without a broker. Every argument is an offset as
// Kafka reports it.
func classifyRetention(oldest, newest, committed int64, riskFraction float64) (state RetentionState, lag, headroom, lost int64) {
	// sarama reports -1 for "this group has no committed offset here".
	if committed < 0 {
		return RetentionUnknown, 0, 0, 0
	}

	lag = newest - committed
	if lag < 0 {
		// A committed offset ahead of the high water mark is not physically
		// meaningful; it shows up transiently while metadata is refreshing.
		// Report zero rather than a negative that would read as "ahead".
		lag = 0
	}

	if committed < oldest {
		// The broker deleted records this group had not reached.
		return RetentionDataLost, lag, 0, oldest - committed
	}

	headroom = committed - oldest
	span := newest - oldest
	if span <= 0 {
		// Nothing retained (empty or fully compacted partition) — there is no
		// trailing edge to fall off.
		return RetentionOK, lag, headroom, 0
	}
	if float64(headroom) < riskFraction*float64(span) {
		return RetentionAtRisk, lag, headroom, 0
	}
	return RetentionOK, lag, headroom, 0
}

// RetentionWatcher samples consumer-group positions against broker retention.
type RetentionWatcher struct {
	client       sarama.Client
	admin        sarama.ClusterAdmin
	topics       []string
	riskFraction float64
}

// NewRetentionWatcher connects to the brokers in KAFKA_BROKERS and watches
// every consumer group that holds a committed offset on any of the given
// topics.
//
// Groups are discovered per sample rather than configured, because they cannot
// all be named ahead of time: Quickwit's group ID embeds a generated ULID
// (`quickwit-pulsetrace-logs:01M07W...-kafka-logs-source`), so a hardcoded list
// would silently omit the one consumer whose backlog the retention cut is most
// likely to endanger. Discovery also means a consumer added later is watched
// without anyone remembering to register it here.
func NewRetentionWatcher(topics []string) (*RetentionWatcher, error) {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_6_0_0

	client, err := sarama.NewClient(brokerList(), cfg)
	if err != nil {
		return nil, fmt.Errorf("retention watcher: connect: %w", err)
	}
	admin, err := sarama.NewClusterAdminFromClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("retention watcher: admin: %w", err)
	}
	return &RetentionWatcher{
		client:       client,
		admin:        admin,
		topics:       topics,
		riskFraction: DefaultRiskFraction,
	}, nil
}

// Snapshot samples every watched group/topic/partition once.
//
// A partition that cannot be read is skipped with a log line rather than
// failing the whole snapshot: a watchdog that goes silent because one broker
// call failed is worse than one reporting partial results, since silence here
// is indistinguishable from health.
func (w *RetentionWatcher) Snapshot(ctx context.Context) ([]PartitionRetention, error) {
	if err := w.client.RefreshMetadata(); err != nil {
		return nil, fmt.Errorf("retention watcher: refresh metadata: %w", err)
	}

	// Partition bounds are fetched once per topic and reused across groups —
	// they are a property of the broker, not of who is reading.
	type bounds struct{ oldest, newest int64 }
	edges := map[string]map[int32]bounds{}
	req := map[string][]int32{}
	for _, topic := range w.topics {
		parts, err := w.client.Partitions(topic)
		if err != nil {
			log.Printf("retention watcher: partitions for %q: %v", topic, err)
			continue
		}
		edges[topic] = map[int32]bounds{}
		for _, p := range parts {
			oldest, err := w.client.GetOffset(topic, p, sarama.OffsetOldest)
			if err != nil {
				log.Printf("retention watcher: oldest offset %s/%d: %v", topic, p, err)
				continue
			}
			newest, err := w.client.GetOffset(topic, p, sarama.OffsetNewest)
			if err != nil {
				log.Printf("retention watcher: newest offset %s/%d: %v", topic, p, err)
				continue
			}
			edges[topic][p] = bounds{oldest, newest}
			req[topic] = append(req[topic], p)
		}
	}
	if len(req) == 0 {
		return nil, nil
	}

	groups, err := w.admin.ListConsumerGroups()
	if err != nil {
		return nil, fmt.Errorf("retention watcher: list consumer groups: %w", err)
	}

	var out []PartitionRetention
	for group := range groups {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		resp, err := w.admin.ListConsumerGroupOffsets(group, req)
		if err != nil {
			log.Printf("retention watcher: offsets for group %q: %v", group, err)
			continue
		}

		for topic, parts := range req {
			for _, p := range parts {
				committed := int64(-1)
				if block := resp.GetBlock(topic, p); block != nil {
					committed = block.Offset
				}
				// A group with no committed offset on this topic is simply not
				// a consumer of it; reporting every group against every topic
				// would bury the three that matter in noise.
				if committed < 0 {
					continue
				}

				b := edges[topic][p]
				state, lag, headroom, lost := classifyRetention(b.oldest, b.newest, committed, w.riskFraction)
				out = append(out, PartitionRetention{
					Group: group, Topic: topic, Partition: p,
					Oldest: b.oldest, Newest: b.newest, Committed: committed,
					Lag: lag, Headroom: headroom, Lost: lost, State: state,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].Topic != out[j].Topic {
			return out[i].Topic < out[j].Topic
		}
		return out[i].Partition < out[j].Partition
	})
	return out, nil
}

// Run samples on an interval until ctx is cancelled, logging anything that is
// not healthy.
//
// Data loss is logged per partition and never aggregated away: "3 partitions
// lost data" is a summary, whereas an operator needs to know which offsets on
// which partition to go and re-ingest.
func (w *RetentionWatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snap, err := w.Snapshot(ctx)
			if err != nil {
				log.Printf("retention watcher: snapshot failed: %v", err)
				continue
			}
			for _, s := range snap {
				switch s.State {
				case RetentionDataLost:
					log.Printf("ALERT kafka retention: group=%s %s/%d LOST %d records "+
						"(committed=%d is behind oldest retained=%d). Kafka deleted them before "+
						"this consumer read them — increase KAFKA_LOG_RETENTION_HOURS or fix the consumer",
						s.Group, s.Topic, s.Partition, s.Lost, s.Committed, s.Oldest)
				case RetentionAtRisk:
					log.Printf("WARN kafka retention: group=%s %s/%d headroom=%d records "+
						"before data loss (lag=%d, retained span=%d)",
						s.Group, s.Topic, s.Partition, s.Headroom, s.Lag, s.Newest-s.Oldest)
				}
			}
		}
	}
}

// Close releases the admin and client connections.
func (w *RetentionWatcher) Close() error {
	var errs []string
	if err := w.admin.Close(); err != nil {
		errs = append(errs, err.Error())
	}
	// NewClusterAdminFromClient does not take ownership of the client, so it is
	// closed separately. sarama returns ErrClosedClient if admin.Close already
	// tore it down, which is not an error worth surfacing.
	if err := w.client.Close(); err != nil && err != sarama.ErrClosedClient {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("retention watcher close: %s", strings.Join(errs, "; "))
	}
	return nil
}
