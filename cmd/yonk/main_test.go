package main

import "testing"

func TestParseRunArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		worker   string
		command  string
		cmdArgs  []string
		exclude  string
		cpu      int
		memoryMB int
		diskMB   int
		timeout  int
		wantFail bool
	}{
		{name: "command", args: []string{"debian", "--", "echo", "hello"}, worker: "debian", command: "echo", cmdArgs: []string{"hello"}},
		{name: "URL", args: []string{"http://worker:8080", "--", "echo"}, worker: "http://worker:8080", command: "echo"},
		{name: "exclude", args: []string{"debian", "--exclude", "vendor", "--", "echo"}, worker: "debian", command: "echo", exclude: "vendor"},
		{name: "resources", args: []string{"debian", "--cpu", "4", "--memory-mb", "2048", "--disk-mb", "1024", "--timeout", "120", "--", "echo"}, worker: "debian", command: "echo", cpu: 4, memoryMB: 2048, diskMB: 1024, timeout: 120},
		{name: "bad timeout", args: []string{"debian", "--timeout", "999", "--", "echo"}, wantFail: true},
		{name: "missing separator", args: []string{"debian", "echo", "hello"}, wantFail: true},
		{name: "missing command", args: []string{"debian", "--"}, wantFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker, command, cmdArgs, options, err := parseRunArgs(test.args)
			if test.wantFail {
				if err == nil {
					t.Fatal("parseRunArgs() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunArgs() error = %v", err)
			}
			if worker != test.worker || command != test.command || !equalStrings(cmdArgs, test.cmdArgs) {
				t.Fatalf("parseRunArgs() = %q, %q, %q", worker, command, cmdArgs)
			}
			if test.exclude != "" && !containsString(options.exclusions, test.exclude) {
				t.Fatalf("parseRunArgs() exclusions = %q, want %q", options.exclusions, test.exclude)
			}
			defaults := defaultRunOptions()
			if options.cpu != test.cpu && test.cpu != 0 {
				t.Fatalf("cpu = %d, want %d", options.cpu, test.cpu)
			}
			if test.cpu == 0 {
				test.cpu = defaults.cpu
			}
			if test.memoryMB == 0 {
				test.memoryMB = defaults.memoryMB
			}
			if test.diskMB == 0 {
				test.diskMB = defaults.diskMB
			}
			if test.timeout == 0 {
				test.timeout = defaults.timeoutSeconds
			}
			if options.cpu != test.cpu || options.memoryMB != test.memoryMB || options.diskMB != test.diskMB || options.timeoutSeconds != test.timeout {
				t.Fatalf("options = %+v, want cpu=%d mem=%d disk=%d timeout=%d", options, test.cpu, test.memoryMB, test.diskMB, test.timeout)
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
