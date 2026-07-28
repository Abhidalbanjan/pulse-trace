package remediation

import (
	"testing"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{in: "off", want: ModeOff},
		{in: "OFF", want: ModeOff},
		{in: "disabled", want: ModeOff},
		{in: "none", want: ModeOff},
		{in: "dry-run", want: ModeDryRun},
		{in: "dry_run", want: ModeDryRun},
		{in: "Dry Run", want: ModeDryRun},
		{in: "  dry-run  ", want: ModeDryRun},
		{in: "plan", want: ModeDryRun},
		{in: "manual", want: ModeManual},
		{in: "approval", want: ModeManual},
		{in: "auto", want: ModeAuto},
		{in: "automatic", want: ModeAuto},

		// A typo must be an error, never a silent fallback — if "dryrun"
		// quietly became ModeAuto, the failure mode is a production outage.
		{in: "dryrun", wantErr: true},
		{in: "yes", wantErr: true},
		{in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMode(%q) = %q, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMode(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefaultPolicyRequiresAHuman(t *testing.T) {
	// The single most important assertion in this package: an unconfigured
	// deployment must not mutate production infrastructure on its own.
	p := DefaultPolicy()
	if p.Mode != ModeManual {
		t.Errorf("default Mode = %q, want %q", p.Mode, ModeManual)
	}
	if got := p.Decide(0.99); got != DecisionPendingApproval {
		t.Errorf("Decide(0.99) under the default policy = %q, want %q", got, DecisionPendingApproval)
	}
}

func TestPolicyDecide(t *testing.T) {
	tests := []struct {
		name       string
		mode       Mode
		threshold  float64
		confidence float64
		want       Decision
	}{
		{"off suppresses regardless of confidence", ModeOff, 0.70, 0.99, DecisionSuppressed},
		{"off suppresses low confidence too", ModeOff, 0.70, 0.10, DecisionSuppressed},

		{"low confidence stays a suggestion in dry-run", ModeDryRun, 0.70, 0.69, DecisionSuggested},
		{"low confidence stays a suggestion in manual", ModeManual, 0.70, 0.10, DecisionSuggested},
		{"low confidence stays a suggestion in auto", ModeAuto, 0.70, 0.69, DecisionSuggested},

		{"dry-run plans at threshold", ModeDryRun, 0.70, 0.70, DecisionDryRun},
		{"manual asks at threshold", ModeManual, 0.70, 0.70, DecisionPendingApproval},
		{"auto executes at threshold", ModeAuto, 0.70, 0.70, DecisionExecute},
		{"auto executes above threshold", ModeAuto, 0.70, 0.95, DecisionExecute},

		{"a custom threshold is honoured", ModeAuto, 0.90, 0.85, DecisionSuggested},
		{"a zero threshold lets everything through", ModeAuto, 0.0, 0.0, DecisionExecute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{Mode: tt.mode, ConfidenceThreshold: tt.threshold}
			if got := p.Decide(tt.confidence); got != tt.want {
				t.Errorf("Decide(%v) = %q, want %q", tt.confidence, got, tt.want)
			}
		})
	}
}

func TestDecideFailsClosedOnAnUnknownMode(t *testing.T) {
	// A Mode that didn't come through ParseMode (zero value, hand-built
	// struct, future mode this build doesn't know) must not be treated as
	// permission to act.
	p := Policy{Mode: Mode("something-new"), ConfidenceThreshold: 0.70}
	if got := p.Decide(0.99); got != DecisionPendingApproval {
		t.Errorf("Decide with an unknown mode = %q, want %q", got, DecisionPendingApproval)
	}

	var zero Policy
	if got := zero.Decide(1.0); got != DecisionPendingApproval {
		t.Errorf("Decide on the zero-value Policy = %q, want %q", got, DecisionPendingApproval)
	}
}

func TestAllowsExecution(t *testing.T) {
	tests := []struct {
		mode Mode
		want bool
	}{
		{ModeOff, false},
		{ModeDryRun, false},
		{ModeManual, true},
		{ModeAuto, true},
		{Mode("bogus"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			if got := (Policy{Mode: tt.mode}).AllowsExecution(); got != tt.want {
				t.Errorf("AllowsExecution() for %q = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestPolicyFromEnv(t *testing.T) {
	t.Run("unset means the safe default", func(t *testing.T) {
		p, err := PolicyFromEnv()
		if err != nil {
			t.Fatalf("PolicyFromEnv: %v", err)
		}
		if p.Mode != ModeManual || p.ConfidenceThreshold != DefaultConfidenceThreshold {
			t.Errorf("PolicyFromEnv() = %+v, want the default policy", p)
		}
	})

	t.Run("mode and threshold are read", func(t *testing.T) {
		t.Setenv("REMEDIATION_MODE", "auto")
		t.Setenv("REMEDIATION_CONFIDENCE_THRESHOLD", "0.85")

		p, err := PolicyFromEnv()
		if err != nil {
			t.Fatalf("PolicyFromEnv: %v", err)
		}
		if p.Mode != ModeAuto {
			t.Errorf("Mode = %q, want auto", p.Mode)
		}
		if p.ConfidenceThreshold != 0.85 {
			t.Errorf("ConfidenceThreshold = %v, want 0.85", p.ConfidenceThreshold)
		}
	})

	t.Run("an invalid mode errors and still returns the safe policy", func(t *testing.T) {
		t.Setenv("REMEDIATION_MODE", "sure-go-ahead")

		p, err := PolicyFromEnv()
		if err == nil {
			t.Fatal("expected an error for an unparseable mode")
		}
		if p.AllowsExecution() && p.Mode == ModeAuto {
			t.Errorf("returned policy = %+v, want a restrictive policy alongside the error", p)
		}
		if p.Mode != ModeManual {
			t.Errorf("Mode = %q, want manual alongside the error", p.Mode)
		}
	})

	t.Run("an out-of-range threshold errors", func(t *testing.T) {
		t.Setenv("REMEDIATION_CONFIDENCE_THRESHOLD", "1.5")
		if _, err := PolicyFromEnv(); err == nil {
			t.Error("expected an error for a threshold outside [0,1]")
		}
	})

	t.Run("a non-numeric threshold errors", func(t *testing.T) {
		t.Setenv("REMEDIATION_CONFIDENCE_THRESHOLD", "high")
		if _, err := PolicyFromEnv(); err == nil {
			t.Error("expected an error for a non-numeric threshold")
		}
	})
}
