package sqladmit

import (
	"strings"
	"unicode"
)

type Result struct {
	Accepted      bool     `json:"accepted"`
	EvidenceLevel string   `json:"evidence_level"`
	ReasonCode    string   `json:"reason_code,omitempty"`
	Signals       []string `json:"signals"`
}

func Admit(sql string) Result {
	tokens, semis, tokenAfterSemicolon, err := scan(sql)
	if err != nil {
		return reject(err.Error())
	}
	if len(tokens) == 0 {
		return reject("empty_sql")
	}
	if semis > 1 || tokenAfterSemicolon {
		return reject("multiple_statements")
	}
	first := tokens[0]
	if first != "SELECT" && first != "WITH" {
		return reject("not_readonly_select")
	}
	joined := " " + strings.Join(tokens, " ") + " "
	for _, forbidden := range []struct{ code, phrase string }{
		{"locking_read", " FOR UPDATE "}, {"locking_read", " LOCK IN SHARE MODE "},
		{"write_or_ddl_statement", " INSERT "}, {"write_or_ddl_statement", " UPDATE "}, {"write_or_ddl_statement", " DELETE "},
		{"write_or_ddl_statement", " CREATE "}, {"write_or_ddl_statement", " ALTER "}, {"write_or_ddl_statement", " DROP "},
		{"select_into_file", " INTO OUTFILE "}, {"select_into_file", " INTO DUMPFILE "},
	} {
		if strings.Contains(joined, forbidden.phrase) {
			return reject(forbidden.code)
		}
	}
	signals := []string{}
	if hasSelectStar(tokens) {
		signals = append(signals, "select_star")
	}
	if strings.Contains(joined, " LIKE '%") {
		signals = append(signals, "leading_wildcard_like")
	}
	if hasFunctionOnColumn(tokens) {
		signals = append(signals, "function_on_probable_column")
	}
	return Result{Accepted: true, EvidenceLevel: "L0", Signals: signals}
}

func reject(code string) Result {
	return Result{EvidenceLevel: "L0", ReasonCode: code, Signals: []string{}}
}

type scanError string

const (
	errUnterminatedLiteralOrComment scanError = "unterminated_literal_or_comment"
	errExecutableComment            scanError = "executable_comment_not_supported"
)

func (e scanError) Error() string { return string(e) }

func scan(s string) ([]string, int, bool, error) {
	// SQL files written by common Windows editors may start with a UTF-8 BOM.
	// It is not part of the SQL statement and must not become the first token.
	s = strings.TrimPrefix(s, "\uFEFF")
	var tokens []string
	var b, literal strings.Builder
	semis := 0
	tokenAfterSemicolon := false
	state := byte(0)
	appendToken := func(token string) {
		if semis > 0 {
			tokenAfterSemicolon = true
		}
		tokens = append(tokens, token)
	}
	flush := func() {
		if b.Len() > 0 {
			appendToken(strings.ToUpper(b.String()))
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch state {
		case '\'':
			if c == '\\' && i+1 < len(s) {
				literal.WriteByte(next)
				i++
				continue
			}
			if c == '\'' && next == '\'' {
				literal.WriteByte(c)
				i++
				continue
			}
			if c == '\'' {
				appendToken("'" + literal.String() + "'")
				literal.Reset()
				state = 0
			} else {
				literal.WriteByte(c)
			}
			continue
		case '"':
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == '"' && next == '"' {
				i++
				continue
			}
			if c == '"' {
				state = 0
			}
			continue
		case '`':
			if c == '`' && next == '`' {
				i++
				continue
			}
			if c == '`' {
				state = 0
			}
			continue
		case '-':
			if c == '\n' {
				state = 0
			}
			continue
		case '/':
			if c == '*' && next == '/' {
				i++
				state = 0
			}
			continue
		}
		if c == '-' && next == '-' && i+2 < len(s) && unicode.IsSpace(rune(s[i+2])) {
			flush()
			i++
			state = '-'
			continue
		}
		if c == '#' {
			flush()
			state = '-'
			continue
		}
		if c == '/' && next == '*' {
			flush()
			if i+2 < len(s) && s[i+2] == '!' {
				return nil, 0, false, errExecutableComment
			}
			i++
			state = '/'
			continue
		}
		if c == '\'' {
			flush()
			state = '\''
			continue
		}
		if c == '"' {
			flush()
			state = '"'
			continue
		}
		if c == '`' {
			flush()
			state = '`'
			continue
		}
		if c == ';' {
			flush()
			semis++
			continue
		}
		if c == '*' {
			flush()
			appendToken("*")
			continue
		}
		if c == '(' || c == ')' {
			flush()
			appendToken(string(c))
			continue
		}
		if unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) || c == '_' || c == '%' {
			b.WriteByte(c)
		} else {
			flush()
		}
	}
	if state == '\'' || state == '"' || state == '`' || state == '/' {
		return nil, 0, false, errUnterminatedLiteralOrComment
	}
	flush()
	return tokens, semis, tokenAfterSemicolon, nil
}

func hasSelectStar(t []string) bool {
	for i := 0; i+1 < len(t); i++ {
		if t[i] == "SELECT" && t[i+1] == "*" {
			return true
		}
	}
	return false
}
func hasFunctionOnColumn(t []string) bool {
	for _, f := range []string{"LOWER", "UPPER", "DATE", "YEAR", "MONTH"} {
		for i := 0; i+2 < len(t); i++ {
			if t[i] == f && t[i+1] == "(" && t[i+2] != ")" {
				return true
			}
		}
	}
	return false
}
