package job

import (
	"strings"
	"testing"
)

func TestJobValidate(t *testing.T) {
	valid := Job{
		Version:        ProtocolVersion,
		ID:             "job_test",
		Command:        "echo",
		Args:           []string{"hello"},
		CWD:            ".",
		Platform:       Platform{OS: "linux", Arch: "amd64"},
		Resources:      Resources{CPU: 1, MemoryMB: 128, DiskMB: 64},
		TimeoutSeconds: 30,
	}

	tests := []struct {
		name   string
		mutate func(*Job)
		want   string
	}{
		{name: "valid", mutate: func(*Job) {}},
		{name: "version", mutate: func(j *Job) { j.Version = 99 }, want: "unsupported job version"},
		{name: "invalid id", mutate: func(j *Job) { j.ID = "job/invalid" }, want: "job id must contain"},
		{name: "empty command", mutate: func(j *Job) { j.Command = "" }, want: "command must contain"},
		{name: "too many arguments", mutate: func(j *Job) { j.Args = make([]string, 129) }, want: "too many arguments"},
		{name: "unsupported cwd", mutate: func(j *Job) { j.CWD = "subdir" }, want: "only supports cwd"},
		{name: "missing platform", mutate: func(j *Job) { j.Platform.OS = "" }, want: "platform"},
		{name: "invalid resources", mutate: func(j *Job) { j.Resources.CPU = 0 }, want: "must be positive"},
		{name: "invalid timeout", mutate: func(j *Job) { j.TimeoutSeconds = 0 }, want: "timeout_seconds"},
		{name: "artifacts", mutate: func(j *Job) { j.Artifacts = []string{"out.txt"} }, want: "does not support artifacts"},
		{name: "network invalid", mutate: func(j *Job) { j.Network = "public" }, want: "unsupported network mode"},
		{name: "network egress", mutate: func(j *Job) { j.Network = "egress" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			test.mutate(&spec)
			err := spec.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}
