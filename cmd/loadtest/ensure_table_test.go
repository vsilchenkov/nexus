package main

import "testing"

func TestDatabaseOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		table string
		want  string
	}{
		{"db_and_table", "nexus_default.loadtest", "nexus_default"},
		{"bare_table", "loadtest", ""},
		{"empty", "", ""},
		{"leading_dot", ".loadtest", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := databaseOf(c.table); got != c.want {
				t.Fatalf("databaseOf(%q) = %q, want %q", c.table, got, c.want)
			}
		})
	}
}
