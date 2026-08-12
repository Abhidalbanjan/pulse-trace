package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pulsetrace/shared/models"
)

// Postmortem drafting (Incidents · E1). Two pure, side-effect-free builders:
//
//   - BuildPostmortemPrompt turns an incident's evidence into an LLM prompt.
//   - DeterministicPostmortem produces a complete Markdown postmortem from the
//     same evidence with no LLM, used as the fallback when no provider is
//     configured (or a generation fails) so the feature never hard-fails.
//
// Both are deterministic given their inputs, so their structure is unit-tested.

// postmortemSections is the required section set; kept in one place so the LLM
// prompt and the deterministic fallback stay in lock-step.
var postmortemSections = []string{"Summary", "Impact", "Timeline", "Root Cause", "Contributing Factors", "Action Items"}

// BuildPostmortemPrompt returns the system + user messages for drafting a
// postmortem. The system message pins the structure and a strict
// ground-in-the-evidence instruction; the user message renders the incident,
// its causal analysis, alerts, and timeline.
func BuildPostmortemPrompt(inc *models.Incident, alerts []models.IncidentAlert, timeline []models.IncidentTimelineEvent) (system, user string) {
	system = "You are an SRE writing a blameless incident postmortem for PulseTrace.\n" +
		"Write in Markdown with exactly these level-2 sections, in this order: " +
		"## " + strings.Join(postmortemSections, ", ## ") + ".\n" +
		"Ground every statement in the evidence provided — do not invent services, times, or causes. " +
		"Reference specific service names and timestamps. Keep it concise and actionable. " +
		"The Action Items section must be a checklist of concrete, assignable follow-ups."

	user = renderIncidentEvidence(inc, alerts, timeline)
	return system, user
}

// renderIncidentEvidence is the shared evidence rendering used by the prompt.
func renderIncidentEvidence(inc *models.Incident, alerts []models.IncidentAlert, timeline []models.IncidentTimelineEvent) string {
	var b strings.Builder
	if inc != nil {
		fmt.Fprintf(&b, "Incident %s — %s\n", inc.ID, inc.Title)
		fmt.Fprintf(&b, "Severity: %s | Status: %s | Services: %s\n",
			inc.Severity, inc.Status, strings.Join(inc.ServiceNames, ", "))
		fmt.Fprintf(&b, "Started: %s", fmtTime(inc.StartedAt))
		if inc.ResolvedAt != nil {
			fmt.Fprintf(&b, " | Resolved: %s | Duration: %s", fmtTime(*inc.ResolvedAt), humanizeDuration(inc.ResolvedAt.Sub(inc.StartedAt)))
		}
		b.WriteString("\n")
		if inc.RootCause != "" {
			fmt.Fprintf(&b, "Inferred root cause: %s\n", inc.RootCause)
		}
		if inc.Causal != nil {
			c := inc.Causal
			fmt.Fprintf(&b, "\nCausal analysis (%s, confidence %.0f%%):\n", c.Model, c.Confidence*100)
			if c.Narrative != "" {
				fmt.Fprintf(&b, "  %s\n", c.Narrative)
			}
			for i, l := range c.Chain {
				fmt.Fprintf(&b, "  %d. %s → %s — %s\n", i+1, l.FromService, l.ToService, l.Evidence)
			}
		}
	}

	b.WriteString("\nAlerts (oldest first):\n")
	sorted := append([]models.IncidentAlert(nil), alerts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TriggeredAt.Before(sorted[j].TriggeredAt) })
	if len(sorted) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, a := range sorted {
		fmt.Fprintf(&b, "  [%s] %s @ %s — %s\n", a.Level, a.ServiceName, fmtTime(a.TriggeredAt), a.Message)
	}

	b.WriteString("\nTimeline:\n")
	if len(timeline) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, e := range timeline {
		svc := ""
		if e.ServiceName != "" {
			svc = " " + e.ServiceName
		}
		fmt.Fprintf(&b, "  %s — %s%s: %s\n", fmtTime(e.At), e.EventType, svc, e.Description)
	}
	return b.String()
}

// DeterministicPostmortem renders a complete Markdown postmortem from evidence
// alone. It is the always-available fallback: correct, grounded, and never
// blocked on an LLM. The LLM path produces richer prose but the same structure.
func DeterministicPostmortem(inc *models.Incident, alerts []models.IncidentAlert, timeline []models.IncidentTimelineEvent) string {
	var b strings.Builder
	title := "Incident"
	if inc != nil && inc.Title != "" {
		title = inc.Title
	}
	fmt.Fprintf(&b, "# Postmortem: %s\n\n", title)

	// Summary
	b.WriteString("## Summary\n\n")
	if inc != nil {
		rc := inc.RootCause
		if inc.Causal != nil && inc.Causal.RootCause != "" {
			rc = inc.Causal.RootCause
		}
		fmt.Fprintf(&b, "A %s-severity incident affecting %s. %s\n\n",
			strings.ToLower(string(inc.Severity)), serviceList(inc.ServiceNames), sentence(rc))
	}

	// Impact
	b.WriteString("## Impact\n\n")
	if inc != nil {
		dur := "ongoing"
		if inc.ResolvedAt != nil {
			dur = humanizeDuration(inc.ResolvedAt.Sub(inc.StartedAt))
		}
		fmt.Fprintf(&b, "- Affected services: %s\n- Severity: %s\n- Duration: %s\n- Alerts in this incident: %d\n\n",
			serviceList(inc.ServiceNames), inc.Severity, dur, inc.AlertCount)
	}

	// Timeline
	b.WriteString("## Timeline\n\n")
	if len(timeline) == 0 {
		b.WriteString("_No timeline events recorded._\n\n")
	} else {
		for _, e := range timeline {
			svc := ""
			if e.ServiceName != "" {
				svc = " **" + e.ServiceName + "**"
			}
			fmt.Fprintf(&b, "- `%s` — %s%s: %s\n", fmtTime(e.At), e.EventType, svc, e.Description)
		}
		b.WriteString("\n")
	}

	// Root Cause
	b.WriteString("## Root Cause\n\n")
	switch {
	case inc != nil && inc.Causal != nil && inc.Causal.Narrative != "":
		fmt.Fprintf(&b, "%s\n\n", inc.Causal.Narrative)
		if len(inc.Causal.Chain) > 0 {
			b.WriteString("Causal chain:\n\n")
			for i, l := range inc.Causal.Chain {
				fmt.Fprintf(&b, "%d. `%s` → `%s` — %s\n", i+1, l.FromService, l.ToService, l.Evidence)
			}
			b.WriteString("\n")
		}
	case inc != nil && inc.RootCause != "":
		fmt.Fprintf(&b, "%s\n\n", sentence(inc.RootCause))
	default:
		b.WriteString("_Root cause not yet determined._\n\n")
	}

	// Contributing Factors — the alerts that composed the incident.
	b.WriteString("## Contributing Factors\n\n")
	sorted := append([]models.IncidentAlert(nil), alerts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TriggeredAt.Before(sorted[j].TriggeredAt) })
	if len(sorted) == 0 {
		b.WriteString("_No alerts recorded._\n\n")
	} else {
		for _, a := range sorted {
			fmt.Fprintf(&b, "- **%s** [%s] — %s (`%s`)\n", a.ServiceName, a.Level, a.Message, fmtTime(a.TriggeredAt))
		}
		b.WriteString("\n")
	}

	// Action Items
	b.WriteString("## Action Items\n\n")
	b.WriteString("- [ ] Confirm the root cause above and add any missing detail\n")
	if inc != nil {
		for _, svc := range inc.ServiceNames {
			fmt.Fprintf(&b, "- [ ] Add/verify monitoring & alerting coverage for `%s`\n", svc)
		}
	}
	b.WriteString("- [ ] Identify and file preventative follow-ups (guardrails, tests, runbooks)\n")
	b.WriteString("- [ ] Assign owners and due dates for each item above\n")

	return b.String()
}

// ── small pure helpers ─────────────────────────────────────────────────────

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

func serviceList(svcs []string) string {
	if len(svcs) == 0 {
		return "unknown services"
	}
	return strings.Join(svcs, ", ")
}

// sentence ensures a non-empty fragment reads as a sentence (capital + period).
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Root cause not yet determined."
	}
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}
