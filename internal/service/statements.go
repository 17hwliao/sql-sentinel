package service

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

type Statement struct {
	SQL                string
	StartLine, EndLine int
}

// Statements preserves the full head-file statement around an edited WHERE
// clause. Delimiters inside strings, identifiers and comments are data.
func Statements(raw string) ([]Statement, error) {
	var result []Statement
	state := byte(0)
	start, line, startLine := 0, 1, 1
	appendStatement := func(end int) {
		text := raw[start:end]
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			leading := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
			result = append(result, Statement{SQL: trimmed, StartLine: startLine + strings.Count(text[:leading], "\n"), EndLine: line})
		}
		start = end
		startLine = line
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		next := byte(0)
		if i+1 < len(raw) {
			next = raw[i+1]
		}
		if c == '\n' {
			line++
		}
		switch state {
		case '\'', '"', '`':
			if c == '\\' && state != '`' && i+1 < len(raw) {
				if next == '\n' {
					line++
				}
				i++
			} else if c == state {
				if next == state {
					i++
				} else {
					state = 0
				}
			}
			continue
		case '-':
			if c == '\n' {
				state = 0
			}
			continue
		case '/':
			if c == '*' && next == '/' {
				state = 0
				i++
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			state = c
			continue
		}
		if c == '#' || (c == '-' && next == '-' && i+2 < len(raw) && strings.ContainsRune(" \t\r\n", rune(raw[i+2]))) {
			state = '-'
			continue
		}
		if c == '/' && next == '*' {
			state = '/'
			i++
			continue
		}
		if c == ';' {
			appendStatement(i + 1)
		}
	}
	if state != 0 && state != '-' {
		return nil, errors.New("unterminated_sql_file")
	}
	appendStatement(len(raw))
	return result, nil
}

var hunk = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

func ChangedLines(patch string) map[int]bool {
	out := map[int]bool{}
	line := 0
	for _, text := range strings.Split(patch, "\n") {
		if match := hunk.FindStringSubmatch(text); match != nil {
			line, _ = strconv.Atoi(match[1])
			continue
		}
		if line == 0 || text == "" || strings.HasPrefix(text, "\\") {
			continue
		}
		if text[0] == '+' {
			out[line] = true
			line++
		} else if text[0] == '-' {
			out[line] = true
			if line > 1 {
				out[line-1] = true
			}
		} else if text[0] == ' ' {
			line++
		}
	}
	return out
}
