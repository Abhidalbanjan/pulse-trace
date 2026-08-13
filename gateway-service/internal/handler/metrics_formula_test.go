package handler

import (
	"math"
	"testing"
)

func TestEvalMetricExpr_Arithmetic(t *testing.T) {
	vars := map[string]float64{"a": 10, "b": 4, "c": 2}
	cases := []struct {
		expr string
		want float64
	}{
		{"a", 10},
		{"a + b", 14},
		{"a - b", 6},
		{"a * c", 20},
		{"a / b", 2.5},
		{"a / b * 100", 250},         // left-to-right, * and / same precedence
		{"a + b * c", 18},            // precedence: b*c first
		{"(a + b) * c", 28},          // parens override
		{"a - b - c", 4},             // left assoc
		{"-a + 20", 10},              // unary minus
		{"a / (b - c)", 5},           // parens in denominator
		{"3.5 * c", 7},               // float literal
	}
	for _, c := range cases {
		got, err := evalMetricExpr(c.expr, vars)
		if err != nil {
			t.Errorf("evalMetricExpr(%q) error: %v", c.expr, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("evalMetricExpr(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMetricExpr_DivZero(t *testing.T) {
	if _, err := evalMetricExpr("a / b", map[string]float64{"a": 1, "b": 0}); err != errDivZero {
		t.Errorf("division by zero should return errDivZero, got %v", err)
	}
}

func TestEvalMetricExpr_RejectsHostileInput(t *testing.T) {
	vars := map[string]float64{"a": 1, "b": 2}
	bad := []string{
		"a; DROP TABLE",     // sql-ish
		"a && b",            // logic ops not allowed
		"a ^ b",             // unsupported op
		"sqrt(a)",           // no functions / multi-letter identifiers
		"a b",               // two vars no operator
		"a +",               // dangling operator
		"(a + b",            // unbalanced paren
		"a)",                // stray close paren
		"1..2",              // malformed number
		"z",                 // undefined series
		"`id`",              // backticks
	}
	for _, expr := range bad {
		if v, err := evalMetricExpr(expr, vars); err == nil {
			t.Errorf("evalMetricExpr(%q) should have errored, got %v", expr, v)
		}
	}
}

func TestReferencedVars(t *testing.T) {
	got := referencedVars("a / b * 100 + a")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("referencedVars = %v, want [a b] deduped+sorted", got)
	}
	if len(referencedVars("42 * 3")) != 0 {
		t.Error("a constant expression references no series")
	}
}
