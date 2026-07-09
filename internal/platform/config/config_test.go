package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		service string
		port    string
		level   string
		dbURL   string
		want    Service
		wantErr string
	}{
		{
			name:    "defaults",
			service: "brain",
			want:    Service{Name: "brain", Port: 8080, LogLevel: "info"},
		},
		{
			name:    "env overrides",
			service: "gateway",
			port:    "9000",
			level:   "debug",
			dbURL:   "postgres://x",
			want:    Service{Name: "gateway", Port: 9000, DatabaseURL: "postgres://x", LogLevel: "debug"},
		},
		{
			name:    "missing service name",
			wantErr: "service name is required",
		},
		{
			name:    "non-numeric port",
			service: "brain",
			port:    "abc",
			wantErr: "invalid PORT",
		},
		{
			name:    "port out of range",
			service: "brain",
			port:    "70000",
			wantErr: "out of range",
		},
		{
			name:    "invalid log level",
			service: "brain",
			level:   "verbose",
			wantErr: "invalid LOG_LEVEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PORT", tt.port)
			t.Setenv("LOG_LEVEL", tt.level)
			t.Setenv("DATABASE_URL", tt.dbURL)

			got, err := Load(tt.service, 8080)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
