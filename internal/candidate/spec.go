package candidate

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const requiredIndexPrefix = "idx_cand_"

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

type Column struct {
	Name      string `json:"name"`
	Direction string `json:"direction,omitempty"`
}

// Spec deliberately contains no raw SQL/DDL field. It is the only form that
// an Agent may hand to the future shadow-database executor.
type Spec struct {
	Table     string   `json:"table"`
	IndexName string   `json:"index_name"`
	Columns   []Column `json:"columns"`
}

func DecodeStrict(r io.Reader) (Spec, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var spec Spec
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode CandidateSpec: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Spec{}, fmt.Errorf("decode CandidateSpec: trailing JSON value")
		}
		return Spec{}, fmt.Errorf("decode CandidateSpec trailing value: %w", err)
	}
	return spec, nil
}

func CompileCreateIndex(spec Spec) (string, error) {
	if err := Validate(spec); err != nil {
		return "", err
	}
	columns := make([]string, 0, len(spec.Columns))
	for _, col := range spec.Columns {
		direction := strings.ToUpper(col.Direction)
		if direction == "" {
			direction = "ASC"
		}
		columns = append(columns, quoteIdentifier(col.Name)+" "+direction)
	}
	return "CREATE INDEX " + quoteIdentifier(spec.IndexName) + " ON " + quoteIdentifier(spec.Table) + " (" + strings.Join(columns, ", ") + ")", nil
}

func Validate(spec Spec) error {
	if err := validateIdentifier("table", spec.Table); err != nil {
		return err
	}
	if err := validateIdentifier("index_name", spec.IndexName); err != nil {
		return err
	}
	if !strings.HasPrefix(spec.IndexName, requiredIndexPrefix) {
		return fmt.Errorf("index_name must start with %q", requiredIndexPrefix)
	}
	if len(spec.Columns) < 1 || len(spec.Columns) > 4 {
		return fmt.Errorf("columns must contain 1 to 4 entries")
	}
	seen := map[string]struct{}{}
	for i, col := range spec.Columns {
		if err := validateIdentifier(fmt.Sprintf("columns[%d].name", i), col.Name); err != nil {
			return err
		}
		if _, ok := seen[col.Name]; ok {
			return fmt.Errorf("columns[%d].name duplicates %q", i, col.Name)
		}
		seen[col.Name] = struct{}{}
		if direction := strings.ToUpper(col.Direction); direction != "" && direction != "ASC" && direction != "DESC" {
			return fmt.Errorf("columns[%d].direction must be ASC or DESC", i)
		}
	}
	return nil
}

func validateIdentifier(field, value string) error {
	if !identifier.MatchString(value) {
		return fmt.Errorf("%s must be a single safe MySQL identifier", field)
	}
	return nil
}

func quoteIdentifier(value string) string { return "`" + value + "`" }
