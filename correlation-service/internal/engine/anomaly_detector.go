package engine

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/pulsetrace/shared/client"
)

type AnomalyDetector struct {
	topoclient *client.TopologyClient
}

func NewAnomalyDetector(topoclient *client.TopologyClient) *AnomalyDetector {
	return &AnomalyDetector{topoclient: topoclient}
}

// Start begins the background polling for metric anomalies.
// In a full implementation, this polls Prometheus PromQL endpoint.
func (a *AnomalyDetector) Start(ctx context.Context) {
	log.Println("anomaly_detector: started background EMA polling")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// EMA variables for payment-service latency
	emaLatency := 50.0 // Starting baseline 50ms
	alpha := 0.2       // Smoothing factor

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Simulate fetching the current P99 latency for payment-service
			currentLatency := 40.0 + (rand.Float64() * 20.0)

			// Randomly simulate a gradual degradation (e.g. connection pool filling up)
			if rand.Float64() < 0.15 { // 15% chance to degrade
				currentLatency += 120.0
			}

			// Calculate Exponential Moving Average (EMA)
			emaLatency = (alpha * currentLatency) + ((1 - alpha) * emaLatency)

			// If EMA crosses statistical threshold (100ms), trigger warning BEFORE hard alerts fire.
			if emaLatency > 100.0 {
				log.Printf("anomaly_detector: ⚠️ Latency EMA spiked to %.1fms on payment-service! Triggering PREDICTIVE_WARNING.", emaLatency)
				
				// Update Neo4j graph state
				if err := a.topoclient.UpdateServiceState(ctx, "payment-service", "PREDICTIVE_WARNING"); err != nil {
					log.Printf("anomaly_detector: failed to update predictive state: %v", err)
				}
				
				// Reset EMA slightly to avoid immediately re-triggering
				emaLatency = 80.0
			} else {
				// Periodically log healthy states to show it's working
				// log.Printf("anomaly_detector: payment-service latency EMA=%.1fms (Healthy)", emaLatency)
			}
		}
	}
}
