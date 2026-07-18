package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/google/uuid"

	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/models"
	"github.com/pulsetrace/shared/rabbitmq"
)

const ruleEvalInterval = 15 * time.Second

// compiledRule caches an alert rule alongside its pre-compiled expr-lang
// program, so a malformed condition is only ever compiled once per refresh
// (and rejected at creation time by gateway-service's validateCondition
// anyway) rather than re-parsed on every single evaluation tick.
type compiledRule struct {
	rule    repository.AlertRule
	program *vm.Program
}

// AlertRuleEvaluator polls user-defined alert rules from Postgres and
// evaluates each one against real per-service RED metrics from ClickHouse —
// this is what makes "user-defined alert rules" real rather than the
// platform's only prior alerting behavior (any ERROR-level log becomes an
// alert, with no way to define a threshold, composite condition, or
// anomaly-based rule at all).
type AlertRuleEvaluator struct {
	repo      *repository.AlertRuleRepository
	metrics   *redMetricsClient
	publisher *rabbitmq.Publisher

	rules     []compiledRule
	baselines map[string]*serviceBaseline // same EWMA approach as AnomalyDetector, kept independently
	lastFired map[string]time.Time        // key: "ruleID:service"
}

func NewAlertRuleEvaluator(repo *repository.AlertRuleRepository, publisher *rabbitmq.Publisher) *AlertRuleEvaluator {
	return &AlertRuleEvaluator{
		repo:      repo,
		metrics:   newRedMetricsClient(),
		publisher: publisher,
		baselines: make(map[string]*serviceBaseline),
		lastFired: make(map[string]time.Time),
	}
}

// Start begins polling: refresh the rule set, fetch metrics, evaluate every
// enabled rule against every matching service.
func (e *AlertRuleEvaluator) Start(ctx context.Context) {
	e.refreshRules(ctx)
	log.Printf("alert_rule_evaluator: started with %d rule(s), polling every %s", len(e.rules), ruleEvalInterval)

	ticker := time.NewTicker(ruleEvalInterval)
	defer ticker.Stop()

	// Reload rules on a slower cadence than metric evaluation, so a new/edited
	// rule takes effect within a minute without hammering Postgres every tick.
	refreshTicker := time.NewTicker(time.Minute)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			e.refreshRules(ctx)
		case <-ticker.C:
			e.evaluateOnce(ctx)
		}
	}
}

// representativeEnv is compiled against, matching the field set evaluateOnce
// always populates at runtime — expr.Compile with expr.Env() only checks
// shape (field names/types), not values, so zero values are fine here.
var representativeEnv = map[string]interface{}{
	"error_rate":     0.0,
	"p50_latency_ms": 0.0,
	"p90_latency_ms": 0.0,
	"p99_latency_ms": 0.0,
	"request_count":  int64(0),
	"error_count":    int64(0),
	"baseline_ratio": 0.0,
}

func (e *AlertRuleEvaluator) refreshRules(ctx context.Context) {
	rules, err := e.repo.ListEnabled(ctx)
	if err != nil {
		log.Printf("alert_rule_evaluator: failed to load rules, keeping previous set: %v", err)
		return
	}

	compiled := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		program, err := expr.Compile(r.Condition, expr.Env(representativeEnv), expr.AsBool())
		if err != nil {
			// Should be rare — gateway-service validates conditions at
			// creation time — but never let one bad rule take down evaluation
			// of every other rule.
			log.Printf("alert_rule_evaluator: rule %q has an invalid condition, skipping: %v", r.Name, err)
			continue
		}
		compiled = append(compiled, compiledRule{rule: r, program: program})
	}
	e.rules = compiled
}

func (e *AlertRuleEvaluator) evaluateOnce(ctx context.Context) {
	if len(e.rules) == 0 {
		return
	}

	rows, err := e.metrics.fetch(ctx)
	if err != nil {
		log.Printf("alert_rule_evaluator: failed to fetch service metrics: %v", err)
		return
	}

	for _, row := range rows {
		if row.Requests < minRequestsToConsider {
			continue
		}

		baseline := e.baselines[row.Service]
		baselineRatio := baseline.ratio(row.P99Ms)
		if baseline != nil && baseline.samples < minSamplesToWarn {
			baselineRatio = 0 // not enough history yet to trust this
		}

		env := map[string]interface{}{
			"error_rate":     row.ErrorRate(),
			"p50_latency_ms": row.P50Ms,
			"p90_latency_ms": row.P90Ms,
			"p99_latency_ms": row.P99Ms,
			"request_count":  row.Requests,
			"error_count":    row.Errors,
			"baseline_ratio": baselineRatio,
		}

		for _, cr := range e.rules {
			if cr.rule.ServiceName != "*" && cr.rule.ServiceName != row.Service {
				continue
			}
			e.evaluateRule(ctx, cr, row.Service, env)
		}

		// Update this service's baseline after evaluating, same pattern as
		// AnomalyDetector — compare against the pre-update baseline, then fold in.
		if baseline == nil {
			e.baselines[row.Service] = &serviceBaseline{ewmaP99Ms: row.P99Ms, samples: 1}
		} else {
			baseline.ewmaP99Ms = (ewmaAlpha * row.P99Ms) + ((1 - ewmaAlpha) * baseline.ewmaP99Ms)
			baseline.samples++
		}
	}
}

func (e *AlertRuleEvaluator) evaluateRule(ctx context.Context, cr compiledRule, serviceName string, env map[string]interface{}) {
	out, err := expr.Run(cr.program, env)
	if err != nil {
		log.Printf("alert_rule_evaluator: rule %q failed to evaluate for %s: %v", cr.rule.Name, serviceName, err)
		return
	}
	matched, _ := out.(bool)
	if !matched {
		return
	}

	cooldownKey := fmt.Sprintf("%s:%s", cr.rule.ID, serviceName)
	cooldown := time.Duration(cr.rule.CooldownSeconds) * time.Second
	if last, ok := e.lastFired[cooldownKey]; ok && time.Since(last) < cooldown {
		return
	}
	e.lastFired[cooldownKey] = time.Now()

	log.Printf("alert_rule_evaluator: 🚨 rule %q matched for %s (error_rate=%.2f%%, p99=%.1fms, baseline_ratio=%.2fx)",
		cr.rule.Name, serviceName, env["error_rate"], env["p99_latency_ms"], env["baseline_ratio"])

	e.publish(ctx, cr.rule, serviceName, env)
}

func (e *AlertRuleEvaluator) publish(ctx context.Context, rule repository.AlertRule, serviceName string, env map[string]interface{}) {
	if e.publisher == nil {
		return
	}

	event := models.NotificationEvent{
		ID:      uuid.New().String(),
		Channel: models.NotificationChannelLog,
		Title:   fmt.Sprintf("[Alert Rule] %s — %s", rule.Name, serviceName),
		Body: fmt.Sprintf(
			"Condition %q matched for %s: error_rate=%.2f%%, p50=%.1fms, p90=%.1fms, p99=%.1fms, baseline_ratio=%.2fx, requests=%v, errors=%v",
			rule.Condition, serviceName, env["error_rate"], env["p50_latency_ms"], env["p90_latency_ms"], env["p99_latency_ms"], env["baseline_ratio"], env["request_count"], env["error_count"],
		),
		Severity:  models.LogLevel(rule.Severity),
		Services:  []string{serviceName},
		CreatedAt: time.Now().UTC(),
	}

	payload, err := json.Marshal(event)
	if err != nil {
		log.Printf("alert_rule_evaluator: failed to marshal notification for rule %q: %v", rule.Name, err)
		return
	}
	if err := e.publisher.Publish(ctx, "incident.notification", payload); err != nil {
		log.Printf("alert_rule_evaluator: rabbitmq publish failed for rule %q: %v", rule.Name, err)
	}
}
