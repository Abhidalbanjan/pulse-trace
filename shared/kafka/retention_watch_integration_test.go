package kafka

import (
	"context"
	"os"
	"testing"
	"time"
)

// Exercises the watcher against a real broker.
//
// The table tests cover the classification, which is where the reasoning lives,
// but they cannot cover the half that talks to Kafka: group discovery, offset
// fetching, and the assumption that a consumer group's committed offsets come
// back keyed the way the code expects. That half is exactly where a wrong
// assumption produces an empty snapshot — and an empty snapshot from a watchdog
// is indistinguishable from a healthy system, which is the worst failure mode
// available to this particular component.
//
// Skipped unless PULSETRACE_KAFKA_IT is set, so it stays out of unit runs:
//
//	PULSETRACE_KAFKA_IT=1 KAFKA_BROKERS=localhost:9092 go test ./kafka/ -run Live -v
func TestRetentionWatcherAgainstLiveBroker(t *testing.T) {
	if os.Getenv("PULSETRACE_KAFKA_IT") == "" {
		t.Skip("set PULSETRACE_KAFKA_IT=1 (and KAFKA_BROKERS) to run against a live broker")
	}

	w, err := NewRetentionWatcher([]string{"logs"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snap, err := w.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap) == 0 {
		t.Fatal("snapshot is empty: no consumer group was found with a committed " +
			"offset on 'logs'. Either discovery is broken or nothing has consumed — " +
			"both make the watchdog silent, which reads as healthy.")
	}

	groups := map[string]int{}
	for _, s := range snap {
		groups[s.Group]++
		if s.Newest < s.Oldest {
			t.Errorf("%s %s/%d: newest %d < oldest %d", s.Group, s.Topic, s.Partition, s.Newest, s.Oldest)
		}
		if s.State == RetentionDataLost {
			t.Errorf("%s %s/%d reports data loss: committed %d behind oldest %d (%d records)",
				s.Group, s.Topic, s.Partition, s.Committed, s.Oldest, s.Lost)
		}
	}
	for g, n := range groups {
		t.Logf("group %-70s partitions=%d", g, n)
	}
}
