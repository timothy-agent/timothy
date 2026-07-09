package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   fstest.MapFS
		want    []string
		wantErr string
	}{
		{
			name: "sorted by number",
			files: fstest.MapFS{
				"0002_gateway.sql": {Data: []byte("select 1;")},
				"0001_init.sql":    {Data: []byte("select 1;")},
				"0010_later.sql":   {Data: []byte("select 1;")},
			},
			want: []string{"0001_init.sql", "0002_gateway.sql", "0010_later.sql"},
		},
		{
			name: "non-sql files ignored",
			files: fstest.MapFS{
				"0001_init.sql": {Data: []byte("select 1;")},
				"embed.go":      {Data: []byte("package migrations")},
			},
			want: []string{"0001_init.sql"},
		},
		{
			name: "malformed sql name rejected",
			files: fstest.MapFS{
				"0001_init.sql": {Data: []byte("select 1;")},
				"init-v2.sql":   {Data: []byte("select 1;")},
			},
			wantErr: "does not match",
		},
		{
			name: "uppercase rejected",
			files: fstest.MapFS{
				"0001_Init.sql": {Data: []byte("select 1;")},
			},
			wantErr: "does not match",
		},
		{
			name:  "empty dir is fine",
			files: fstest.MapFS{},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := List(tt.files)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("List() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("List() unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("List() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("List()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
