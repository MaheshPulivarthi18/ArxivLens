package db

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrationFiles(t *testing.T) {
	tests := []struct {
		name    string
		files   []string // created empty in a temp dir; a trailing "/" makes a directory
		want    []string // base names, in order
		wantErr bool
	}{
		{
			name:  "up only, lexical order regardless of creation order",
			files: []string{"000002_b.up.sql", "000002_b.down.sql", "000001_a.down.sql", "000001_a.up.sql"},
			want:  []string{"000001_a.up.sql", "000002_b.up.sql"},
		},
		{
			name:  "ignores non-sql, plain .sql and directories named like migrations",
			files: []string{"README.md", "000003_c.sql", "000004_d.up.sql/", "000001_a.up.sql", "000010_j.up.sql", "000009_i.up.sql"},
			want:  []string{"000001_a.up.sql", "000009_i.up.sql", "000010_j.up.sql"},
		},
		{
			name:    "no up migrations is an error",
			files:   []string{"000001_a.down.sql"},
			wantErr: true,
		},
		{
			name:    "empty dir is an error",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				p := filepath.Join(dir, f)
				var err error
				if f[len(f)-1] == '/' {
					err = os.Mkdir(p, 0o755)
				} else {
					err = os.WriteFile(p, []byte("-- test\n"), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			got, err := MigrationFiles(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := make([]string, len(tc.want))
			for i, n := range tc.want {
				want[i] = filepath.Join(dir, n)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestMigrationFilesMissingDir(t *testing.T) {
	if _, err := MigrationFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for missing dir")
	}
}

// The repo's own migrations dir must apply 000001 before the HNSW index in 000002.
func TestRepoMigrationsOrder(t *testing.T) {
	got, err := MigrationFiles(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"000001_init_schema.up.sql", "000002_chunk_embeddings_hnsw.up.sql", "000003_paper_chunk_pages.up.sql"}
	if len(got) < len(want) {
		t.Fatalf("got %v, want prefix %v", got, want)
	}
	for i, n := range want {
		if filepath.Base(got[i]) != n {
			t.Fatalf("position %d: got %s, want %s (all: %v)", i, filepath.Base(got[i]), n, got)
		}
	}
}
