package sandboxd

import "testing"

func TestResolveBackend(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		k8sHost  string
		want     string
		wantErr  bool
	}{
		{name: "default docker", want: BackendDocker},
		{name: "auto kubernetes in cluster", k8sHost: "10.96.0.1", want: BackendKubernetes},
		{name: "explicit docker in cluster", explicit: "docker", k8sHost: "10.96.0.1", want: BackendDocker},
		{name: "explicit kubernetes outside cluster", explicit: "kubernetes", want: BackendKubernetes},
		{name: "unknown fails closed", explicit: "podman", wantErr: true},
		{name: "case sensitive", explicit: "Docker", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBackend(tc.explicit, tc.k8sHost)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveBackend(%q, %q) = %q, want error", tc.explicit, tc.k8sHost, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveBackend(%q, %q): %v", tc.explicit, tc.k8sHost, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveBackend(%q, %q) = %q, want %q", tc.explicit, tc.k8sHost, got, tc.want)
			}
		})
	}
}

// Compile-time proof that the Docker backend satisfies Backend.
var _ Backend = (*Docker)(nil)
