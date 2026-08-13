package client

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "hostname", input: "debian", want: "http://debian:9665"},
		{name: "host and port", input: "debian:9000", want: "http://debian:9000"},
		{name: "https", input: "https://worker.example:443", want: "https://worker.example:443"},
		{name: "IPv6", input: "http://[::1]", want: "http://[::1]:9665"},
		{name: "path rejected", input: "http://debian/path", wantErr: true},
		{name: "scheme rejected", input: "ftp://debian", wantErr: true},
		{name: "empty rejected", input: "", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NormalizeEndpoint() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEndpoint() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NormalizeEndpoint() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNewJobID(t *testing.T) {
	first, err := NewJobID()
	if err != nil {
		t.Fatalf("NewJobID() error = %v", err)
	}
	second, err := NewJobID()
	if err != nil {
		t.Fatalf("NewJobID() second error = %v", err)
	}
	if first == second || len(first) != len("job_")+24 {
		t.Fatalf("unexpected job IDs %q and %q", first, second)
	}
}
