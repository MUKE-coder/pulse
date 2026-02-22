package pulse

import "testing"

func TestNormalizeSQL(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantNorm   string
		wantOp     string
		wantTable  string
	}{
		{
			name:      "simple select with number",
			input:     "SELECT * FROM users WHERE id = 42",
			wantNorm:  "select * from users where id = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "select with string literal",
			input:     "SELECT * FROM users WHERE name = 'John'",
			wantNorm:  "select * from users where name = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "mixed literals",
			input:     "SELECT * FROM users WHERE id = 42 AND name = 'John'",
			wantNorm:  "select * from users where id = ? and name = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "insert statement",
			input:     "INSERT INTO posts (title, body) VALUES ('Hello', 'World')",
			wantNorm:  "insert into posts (title, body) values (?, ?)",
			wantOp:    "INSERT",
			wantTable: "posts",
		},
		{
			name:      "update statement",
			input:     "UPDATE users SET name = 'Jane' WHERE id = 5",
			wantNorm:  "update users set name = ? where id = ?",
			wantOp:    "UPDATE",
			wantTable: "users",
		},
		{
			name:      "delete statement",
			input:     "DELETE FROM comments WHERE id = 100",
			wantNorm:  "delete from comments where id = ?",
			wantOp:    "DELETE",
			wantTable: "comments",
		},
		{
			name:      "IN list",
			input:     "SELECT * FROM users WHERE id IN (1, 2, 3, 4, 5)",
			wantNorm:  "select * from users where id in (?)",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "extra whitespace",
			input:     "SELECT   *   FROM   users   WHERE   id  =  1",
			wantNorm:  "select * from users where id = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "float literal",
			input:     "SELECT * FROM products WHERE price > 19.99",
			wantNorm:  "select * from products where price > ?",
			wantOp:    "SELECT",
			wantTable: "products",
		},
		{
			name:      "backtick table name",
			input:     "SELECT * FROM `users` WHERE id = 1",
			wantNorm:  "select * from `users` where id = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "empty string",
			input:     "",
			wantNorm:  "",
			wantOp:    "",
			wantTable: "",
		},
		{
			name:      "escaped quote in string",
			input:     "SELECT * FROM users WHERE name = 'O''Brien'",
			wantNorm:  "select * from users where name = ?",
			wantOp:    "SELECT",
			wantTable: "users",
		},
		{
			name:      "identifiers with numbers not replaced",
			input:     "SELECT col1, col2 FROM table1 WHERE col3 = 42",
			wantNorm:  "select col1, col2 from table1 where col3 = ?",
			wantOp:    "SELECT",
			wantTable: "table1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeSQL(tt.input)
			if got.Normalized != tt.wantNorm {
				t.Errorf("Normalized:\n  got:  %q\n  want: %q", got.Normalized, tt.wantNorm)
			}
			if got.Operation != tt.wantOp {
				t.Errorf("Operation: got %q, want %q", got.Operation, tt.wantOp)
			}
			if got.Table != tt.wantTable {
				t.Errorf("Table: got %q, want %q", got.Table, tt.wantTable)
			}
		})
	}
}
