// Package taskbug implements a tiny CSV-line parser used by the
// scheduling system. ParseLine takes a single CSV row and returns the
// fields as strings. Commas inside double-quoted fields must be
// preserved; a doubled "" inside a quoted field is an escaped quote.
//
// Edge cases:
//   - empty input → []string{""}
//   - trailing comma → empty field at end
//   - mismatched quote → error
package taskbug

import (
	"errors"
	"strings"
)

// ParseLine parses one CSV row into fields.
func ParseLine(line string) ([]string, error) {
	var out []string
	var sb strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"' && !inQuotes:
			inQuotes = true
		case c == '"' && inQuotes:
			// BUG: doesn't handle doubled "" — treats it as end of quote
			inQuotes = false
		case c == ',' && !inQuotes:
			out = append(out, sb.String())
			sb.Reset()
		default:
			sb.WriteByte(c)
		}
	}
	if inQuotes {
		return nil, errors.New("unterminated quoted field")
	}
	out = append(out, sb.String())
	return out, nil
}
