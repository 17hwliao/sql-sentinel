package sqladmit

import (
	"errors"
	"sort"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// Inspect parses MySQL syntax, rejects side effects and returns physical tables
// in the current shadow schema. Identifiers inside quotes are inspected too.
type Analysis struct {
	Tables                  []string
	Ordered, UnorderedLimit bool
}

func Inspect(query string) ([]string, error) { a, err := Analyze(query); return a.Tables, err }

// Analyze performs one bounded AST traversal for admission, physical scope and
// result ordering. Rewrite admission uses the same parsed safety information.
func Analyze(query string) (Analysis, error) {
	if len(query) > 64<<10 {
		return Analysis{}, errors.New("sql_limit_exceeded")
	}
	nodes, _, err := parser.New().ParseSQL(strings.TrimPrefix(query, "\uFEFF"))
	if err != nil || len(nodes) != 1 {
		return Analysis{}, errors.New("invalid_sql_syntax")
	}
	switch nodes[0].(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
	default:
		return Analysis{}, errors.New("not_readonly_select")
	}
	v := &safetyVisitor{tables: map[string]bool{}, pushed: map[ast.Node]bool{}}
	nodes[0].Accept(v)
	if v.err != nil {
		return Analysis{}, v.err
	}
	tables := make([]string, 0, len(v.tables))
	for name := range v.tables {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	ordered := false
	switch n := nodes[0].(type) {
	case *ast.SelectStmt:
		ordered = n.OrderBy != nil
	case *ast.SetOprStmt:
		ordered = n.OrderBy != nil
	}
	return Analysis{Tables: tables, Ordered: ordered, UnorderedLimit: v.unorderedLimit}, nil
}
func OrderSensitive(query string) bool {
	a, err := Analyze(query)
	return err != nil || a.Ordered
}

type safetyVisitor struct {
	tables         map[string]bool
	scopes         []map[string]bool
	err            error
	count          int
	pushed         map[ast.Node]bool
	unorderedLimit bool
}

func (v *safetyVisitor) Enter(n ast.Node) (ast.Node, bool) {
	v.count++
	if v.count > 4096 {
		v.err = errors.New("sql_complexity_exceeded")
		return n, true
	}
	fail := func(code string) {
		if v.err == nil {
			v.err = errors.New(code)
		}
	}
	switch x := n.(type) {
	case *ast.SelectStmt:
		if x.Limit != nil && x.OrderBy == nil {
			v.unorderedLimit = true
		}
		v.push()
		v.pushed[n] = true
		if x.LockInfo != nil && x.LockInfo.LockType != ast.SelectLockNone {
			fail("locking_read")
		}
		if x.SelectIntoOpt != nil {
			fail("select_into_not_supported")
		}
		if len(x.TableHints) > 0 {
			fail("optimizer_hint_not_supported")
		}
	case *ast.WithClause:
		if x.IsRecursive {
			fail("recursive_cte_not_supported")
		}
	case *ast.SetOprStmt:
		if x.Limit != nil && x.OrderBy == nil {
			v.unorderedLimit = true
		}
		v.push()
		v.pushed[n] = true
	case *ast.TableName:
		if x.Schema.O != "" {
			fail("cross_schema_not_supported")
		}
		name := strings.ToLower(x.Name.O)
		cte := false
		for _, scope := range v.scopes {
			if scope[name] {
				cte = true
			}
		}
		if !cte {
			v.tables[name] = true
		}
	case *ast.VariableExpr:
		fail("session_variable_not_supported")
	case *ast.FuncCallExpr:
		if x.Schema.O != "" || !safeFunctions[strings.ToUpper(x.FnName.O)] {
			fail("unsafe_or_unknown_function")
		}
	}
	return n, v.err != nil
}
func (v *safetyVisitor) Leave(n ast.Node) (ast.Node, bool) {
	if v.pushed[n] {
		v.scopes = v.scopes[:len(v.scopes)-1]
		delete(v.pushed, n)
	}
	// A nonrecursive CTE becomes visible only AFTER its definition. Its own
	// name and later CTE names inside that definition still resolve to physical
	// tables (or already visible outer CTEs), and must stay in the table fence.
	if cte, ok := n.(*ast.CommonTableExpression); ok && len(v.scopes) > 0 {
		v.scopes[len(v.scopes)-1][strings.ToLower(cte.Name.O)] = true
	}
	return n, v.err == nil
}
func (v *safetyVisitor) push() {
	v.scopes = append(v.scopes, map[string]bool{})
}

// Unknown/UDF/stored functions fail closed. Aggregate/window functions are
// separate AST nodes and cannot resolve to user-defined side-effecting routines.
var safeFunctions = func() map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Fields("ABS CEIL CEILING FLOOR ROUND TRUNCATE MOD POW POWER SQRT SIGN GREATEST LEAST COALESCE IF IFNULL NULLIF CONCAT CONCAT_WS LENGTH CHAR_LENGTH CHARACTER_LENGTH LOWER LCASE UPPER UCASE TRIM LTRIM RTRIM SUBSTRING SUBSTR LEFT RIGHT REPLACE INSTR LOCATE LPAD RPAD REVERSE ASCII ORD HEX UNHEX DATE YEAR MONTH DAY DAYOFMONTH DAYOFWEEK HOUR MINUTE SECOND DATE_FORMAT DATEDIFF TIMESTAMPDIFF TIMESTAMPADD DATE_ADD DATE_SUB ADDDATE SUBDATE NOW CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP UTC_TIMESTAMP CAST CONVERT JSON_EXTRACT JSON_UNQUOTE JSON_LENGTH JSON_CONTAINS JSON_VALID") {
		out[name] = true
	}
	return out
}()
