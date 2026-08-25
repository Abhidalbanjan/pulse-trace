package sqlq

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // value expression impl; required by the parser
)

// Reason classifies why a statement was refused, so the HTTP layer can answer
// with something a user can act on and tests can assert on the cause rather
// than on message wording.
type Reason string

const (
	ReasonSyntax           Reason = "syntax"
	ReasonTooLarge         Reason = "statement_too_large"
	ReasonMultipleStmts    Reason = "multiple_statements"
	ReasonNotSelect        Reason = "not_a_select"
	ReasonUnknownRelation  Reason = "unknown_relation"
	ReasonQualifiedName    Reason = "qualified_name"
	ReasonDeniedFunction   Reason = "denied_function"
	ReasonIntoOutfile      Reason = "into_outfile"
	ReasonLocking          Reason = "locking_read"
	ReasonTooManyJoins     Reason = "too_many_joins"
	ReasonTooDeep          Reason = "subquery_too_deep"
	ReasonTooManyBranches  Reason = "too_many_set_branches"
	ReasonShadowedRelation Reason = "cte_shadows_relation"
)

// RejectionError is returned for every refusal.
type RejectionError struct {
	Reason Reason
	Detail string
	Hint   string // optional, actionable
}

func (e *RejectionError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Reason, e.Detail, e.Hint)
	}
	return fmt.Sprintf("%s: %s", e.Reason, e.Detail)
}

func reject(r Reason, detail string, hint ...string) *RejectionError {
	e := &RejectionError{Reason: r, Detail: detail}
	if len(hint) > 0 {
		e.Hint = hint[0]
	}
	return e
}

// ReasonOf extracts the rejection reason from an error, if it is one.
func ReasonOf(err error) (Reason, bool) {
	var re *RejectionError
	if errors.As(err, &re) {
		return re.Reason, true
	}
	return "", false
}

// Policy bounds the accepted grammar.
//
// These are not performance tuning. Each limit removes a class of query whose
// cost or blast radius is hard to reason about, and the cheapest way to keep a
// query engine safe is to keep the set of accepted queries small enough that
// its behaviour can be enumerated.
type Policy struct {
	MaxJoins          int
	MaxSubqueryDepth  int
	MaxSetOpBranches  int
	MaxStatementBytes int
}

// DefaultPolicy is deliberately tight. Loosening a limit is a reviewable change
// with a stated reason; starting loose and tightening after an incident is not
// a plan.
func DefaultPolicy() Policy {
	return Policy{
		MaxJoins:          4,
		MaxSubqueryDepth:  3,
		MaxSetOpBranches:  4,
		MaxStatementBytes: 16 * 1024,
	}
}

// deniedFunctions are refused wherever they appear.
//
// Most of these cannot even parse under this grammar — ClickHouse's file(),
// url(), remote() and s3() table functions are not valid in a MySQL FROM
// clause, so the parser rejects them first. They are listed anyway because the
// grammar in front is an implementation detail that could be swapped, while
// "this function must never run" is a property of the product. A denial that
// only works because of the current parser is a denial that disappears quietly
// when the parser changes.
var deniedFunctions = map[string]string{
	"file":         "filesystem access",
	"url":          "network access",
	"remote":       "network access",
	"remotesecure": "network access",
	"s3":           "network access",
	"hdfs":         "network access",
	"mysql":        "network access",
	"postgresql":   "network access",
	"jdbc":         "network access",
	"odbc":         "network access",
	"executable":   "process execution",
	"load_file":    "filesystem access",
	"sleep":        "denial of service",
	"benchmark":    "denial of service",
	"get_lock":     "lock acquisition",
	"sys_exec":     "process execution",
	"sys_eval":     "process execution",
}

// Analysis is what a caller gets for an accepted statement.
type Analysis struct {
	// Relations are the catalog relations the statement reads, deduplicated.
	// The planner turns each into a tenant-bound scanner; nothing outside this
	// list can be reached, because nothing outside it resolved.
	Relations []Relation
	// Stmt is the validated AST. Callers must plan from this, never from the
	// original text: re-parsing user text downstream reintroduces exactly the
	// parser-differential this package exists to remove.
	Stmt ast.StmtNode
}

// Stores returns the distinct stores the statement touches.
func (a *Analysis) Stores() []Store {
	seen := map[Store]bool{}
	var out []Store
	for _, r := range a.Relations {
		if !seen[r.Store] {
			seen[r.Store] = true
			out = append(out, r.Store)
		}
	}
	return out
}

// Validate parses and checks one user-authored statement.
//
// It returns either an *Analysis naming every relation the statement reads, or
// a *RejectionError. There is no third outcome and no "accepted with warnings":
// a query the policy cannot fully account for is refused.
func Validate(sql string, cat *Catalog, p Policy) (*Analysis, error) {
	if len(sql) > p.MaxStatementBytes {
		return nil, reject(ReasonTooLarge,
			fmt.Sprintf("statement is %d bytes, limit is %d", len(sql), p.MaxStatementBytes))
	}

	stmts, _, err := parser.New().Parse(sql, "", "")
	if err != nil {
		return nil, reject(ReasonSyntax, err.Error())
	}
	switch len(stmts) {
	case 0:
		return nil, reject(ReasonSyntax, "no statement found")
	case 1:
	default:
		// Stacked statements. Refused even when every statement would
		// individually pass, because "run exactly one query" is the contract the
		// budget, audit and result plumbing are all written against.
		return nil, reject(ReasonMultipleStmts,
			fmt.Sprintf("got %d statements, exactly one is allowed", len(stmts)),
			"send one query per request")
	}

	stmt := stmts[0]
	switch stmt.(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
	default:
		// Everything else — INSERT, UPDATE, DELETE, every DDL, SET, SHOW, USE,
		// EXPLAIN, administrative statements — is refused by exclusion. An
		// allowlist over statement types means a statement kind added by a future
		// parser upgrade is refused by default rather than admitted by silence.
		return nil, reject(ReasonNotSelect,
			fmt.Sprintf("%T is not permitted", stmt),
			"only SELECT is supported")
	}

	v := &validator{cat: cat, policy: p, cteNames: map[string]bool{}}
	stmt.Accept(v)
	if v.err != nil {
		return nil, v.err
	}
	return &Analysis{Relations: v.relations(), Stmt: stmt}, nil
}

type validator struct {
	cat      *Catalog
	policy   Policy
	err      *RejectionError
	depth    int
	joins    int
	seen     map[string]Relation
	cteNames map[string]bool
}

func (v *validator) fail(e *RejectionError) {
	if v.err == nil {
		v.err = e // first rejection wins; later ones are noise from a doomed walk
	}
}

func (v *validator) Enter(n ast.Node) (ast.Node, bool) {
	if v.err != nil {
		return n, true // stop descending once refused
	}

	switch node := n.(type) {

	case *ast.SelectStmt:
		v.depth++
		if v.depth > v.policy.MaxSubqueryDepth {
			v.fail(reject(ReasonTooDeep,
				fmt.Sprintf("nesting depth %d exceeds limit %d", v.depth, v.policy.MaxSubqueryDepth)))
			return n, true
		}
		if node.SelectIntoOpt != nil {
			// INTO OUTFILE / DUMPFILE writes to the server's filesystem.
			v.fail(reject(ReasonIntoOutfile, "INTO OUTFILE/DUMPFILE is not permitted"))
			return n, true
		}
		if node.LockInfo != nil {
			v.fail(reject(ReasonLocking, "locking reads (FOR UPDATE / LOCK IN SHARE MODE) are not permitted"))
			return n, true
		}

	case *ast.SetOprStmt:
		// UNION / INTERSECT / EXCEPT. Branch count is bounded because each branch
		// is an independent scan, and a set operation is the natural shape for
		// "read a lot while looking like one query".
		if node.SelectList != nil && len(node.SelectList.Selects) > v.policy.MaxSetOpBranches {
			v.fail(reject(ReasonTooManyBranches,
				fmt.Sprintf("%d branches exceeds limit %d", len(node.SelectList.Selects), v.policy.MaxSetOpBranches)))
			return n, true
		}

	case *ast.WithClause:
		// A CTE may not take the name of a catalog relation.
		//
		// Shadowing is refused rather than resolved. Tenant binding happens per
		// catalog relation, so a name that means the relation in one place and
		// the CTE in another is precisely the ambiguity an isolation bug hides
		// in — and no user needs to call their CTE `logs`.
		for _, cte := range node.CTEs {
			name := strings.ToLower(cte.Name.L)
			if _, clash := v.cat.Lookup("", name); clash {
				v.fail(reject(ReasonShadowedRelation,
					fmt.Sprintf("CTE %q has the same name as a catalog relation", name),
					"rename the CTE"))
				return n, true
			}
			v.cteNames[name] = true
		}

	case *ast.Join:
		// Counted on the node, not on table references, so that a join expressed
		// through a comma list is bounded the same way as an explicit JOIN.
		if node.Right != nil {
			v.joins++
			if v.joins > v.policy.MaxJoins {
				v.fail(reject(ReasonTooManyJoins,
					fmt.Sprintf("%d joins exceeds limit %d", v.joins, v.policy.MaxJoins)))
				return n, true
			}
		}

	case *ast.TableName:
		v.resolve(node)

	case *ast.FuncCallExpr:
		if why, denied := deniedFunctions[strings.ToLower(node.FnName.L)]; denied {
			v.fail(reject(ReasonDeniedFunction,
				fmt.Sprintf("function %q is not permitted (%s)", node.FnName.O, why)))
			return n, true
		}
	}

	return n, false
}

func (v *validator) Leave(n ast.Node) (ast.Node, bool) {
	if _, ok := n.(*ast.SelectStmt); ok {
		v.depth--
	}
	return n, v.err == nil
}

// resolve maps one table reference onto the catalog, or refuses it.
func (v *validator) resolve(t *ast.TableName) {
	schema := t.Schema.L
	name := strings.ToLower(t.Name.L)

	// A reference to a CTE defined in this statement is not a catalog lookup.
	// CTE names cannot collide with relations (checked above), so this cannot be
	// used to smuggle a relation reference past resolution.
	if schema == "" && v.cteNames[name] {
		return
	}

	if schema != "" {
		// Qualified names are refused outright rather than resolved-and-checked.
		// `system.tables`, `information_schema.columns`, `pulsetrace.otel_logs`
		// and `mysql.user` all fail here for the same structural reason: the
		// catalog has no notion of a schema, so there is nothing for a qualifier
		// to select between.
		v.fail(reject(ReasonQualifiedName,
			fmt.Sprintf("qualified name %q.%q is not permitted", t.Schema.O, t.Name.O),
			"reference relations by bare name, e.g. logs"))
		return
	}

	rel, ok := v.cat.Lookup("", name)
	if !ok {
		v.fail(reject(ReasonUnknownRelation,
			fmt.Sprintf("unknown relation %q", t.Name.O),
			"available: "+strings.Join(v.cat.Names(), ", ")))
		return
	}
	if v.seen == nil {
		v.seen = map[string]Relation{}
	}
	v.seen[rel.Name] = rel
}

func (v *validator) relations() []Relation {
	out := make([]Relation, 0, len(v.seen))
	for _, r := range v.seen {
		out = append(out, r)
	}
	// Stable order so plans, audit records and tests do not depend on map order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
