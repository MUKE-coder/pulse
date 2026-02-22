package pulse

import (
	"strings"
	"unicode"
)

// NormalizedQuery holds the result of SQL normalization.
type NormalizedQuery struct {
	Normalized string
	Operation  string
	Table      string
}

// NormalizeSQL normalizes a SQL query for pattern grouping:
//   - Replaces string literals ('value') with ?
//   - Replaces numeric literals with ?
//   - Replaces IN (...) lists with IN (?)
//   - Lowercases SQL keywords
//   - Collapses extra whitespace
//   - Extracts the operation (SELECT, INSERT, UPDATE, DELETE) and table name
func NormalizeSQL(sql string) NormalizedQuery {
	if sql == "" {
		return NormalizedQuery{}
	}

	normalized := replaceStringLiterals(sql)
	normalized = replaceNumericLiterals(normalized)
	normalized = collapseINLists(normalized)
	normalized = collapseWhitespace(normalized)
	normalized = strings.TrimSpace(normalized)
	normalized = strings.ToLower(normalized)

	op, table := extractOperationAndTable(normalized)

	return NormalizedQuery{
		Normalized: normalized,
		Operation:  op,
		Table:      table,
	}
}

// replaceStringLiterals replaces 'value' with ?
func replaceStringLiterals(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))

	inString := false
	escaped := false

	for i := 0; i < len(sql); i++ {
		ch := sql[i]

		if escaped {
			escaped = false
			if inString {
				continue // skip escaped char inside string
			}
			b.WriteByte(ch)
			continue
		}

		if ch == '\\' {
			escaped = true
			if !inString {
				b.WriteByte(ch)
			}
			continue
		}

		if ch == '\'' {
			if inString {
				// Check for escaped quote ''
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++ // skip next quote
					continue
				}
				inString = false
				b.WriteByte('?')
				continue
			}
			inString = true
			continue
		}

		if !inString {
			b.WriteByte(ch)
		}
	}

	return b.String()
}

// replaceNumericLiterals replaces standalone numeric values with ?
func replaceNumericLiterals(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))

	i := 0
	for i < len(sql) {
		ch := sql[i]

		// Check if we're at the start of a number
		if isDigit(ch) || (ch == '-' && i+1 < len(sql) && isDigit(sql[i+1])) {
			// Check what's before — if it's a letter or underscore, this is part of an identifier
			if i > 0 && (isIdentChar(sql[i-1])) {
				b.WriteByte(ch)
				i++
				continue
			}

			// Skip the sign if present
			start := i
			if ch == '-' {
				i++
			}

			// Consume digits and optional decimal
			hasDigits := false
			for i < len(sql) && isDigit(sql[i]) {
				i++
				hasDigits = true
			}
			if i < len(sql) && sql[i] == '.' {
				i++
				for i < len(sql) && isDigit(sql[i]) {
					i++
				}
			}

			if !hasDigits {
				// Just a minus sign, not a number
				b.WriteByte(sql[start])
				i = start + 1
				continue
			}

			// Check what's after — if it's a letter or underscore, this was part of an identifier
			if i < len(sql) && isIdentChar(sql[i]) {
				b.WriteString(sql[start:i])
				continue
			}

			b.WriteByte('?')
			continue
		}

		b.WriteByte(ch)
		i++
	}

	return b.String()
}

// collapseINLists replaces IN (?, ?, ?) with IN (?)
func collapseINLists(sql string) string {
	upper := strings.ToUpper(sql)
	var b strings.Builder
	b.Grow(len(sql))

	i := 0
	for i < len(sql) {
		// Look for "IN" keyword followed by "("
		if i+2 < len(sql) && upper[i] == 'I' && upper[i+1] == 'N' {
			// Check it's word-boundary before IN
			if i > 0 && isIdentChar(sql[i-1]) {
				b.WriteByte(sql[i])
				i++
				continue
			}

			// Skip whitespace after IN
			j := i + 2
			for j < len(sql) && sql[j] == ' ' {
				j++
			}

			if j < len(sql) && sql[j] == '(' {
				// Find closing paren
				k := j + 1
				depth := 1
				for k < len(sql) && depth > 0 {
					if sql[k] == '(' {
						depth++
					} else if sql[k] == ')' {
						depth--
					}
					k++
				}

				if depth == 0 {
					b.WriteString(sql[i : i+2]) // "IN"
					b.WriteString(sql[i+2 : j]) // whitespace
					b.WriteString("(?)")
					i = k
					continue
				}
			}
		}

		b.WriteByte(sql[i])
		i++
	}

	return b.String()
}

// collapseWhitespace replaces runs of whitespace with single space.
func collapseWhitespace(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))

	prevSpace := false
	for _, ch := range sql {
		if unicode.IsSpace(ch) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(ch)
			prevSpace = false
		}
	}

	return b.String()
}

// extractOperationAndTable extracts the SQL operation and primary table name
// from a lowercased, normalized SQL string.
func extractOperationAndTable(normalized string) (string, string) {
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return "", ""
	}

	op := strings.ToUpper(fields[0])

	switch op {
	case "SELECT":
		// SELECT ... FROM table ...
		return op, findWordAfter(fields, "from")
	case "INSERT":
		// INSERT INTO table ...
		return op, findWordAfter(fields, "into")
	case "UPDATE":
		// UPDATE table SET ...
		if len(fields) > 1 {
			return op, cleanTableName(fields[1])
		}
	case "DELETE":
		// DELETE FROM table ...
		return op, findWordAfter(fields, "from")
	}

	return op, ""
}

// findWordAfter finds the word immediately following the given keyword.
func findWordAfter(fields []string, keyword string) string {
	for i, f := range fields {
		if f == keyword && i+1 < len(fields) {
			return cleanTableName(fields[i+1])
		}
	}
	return ""
}

// cleanTableName removes common decorations from table names.
func cleanTableName(name string) string {
	// Remove backticks and quotes
	name = strings.Trim(name, "`\"")
	// Remove schema prefix (e.g., "public.users" -> "users")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isIdentChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || (ch >= '0' && ch <= '9')
}
