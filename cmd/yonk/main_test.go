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
		wantFail bool
	}{
		{name: "command", args: []string{"debian", "--", "echo", "hello"}, worker: "debian", command: "echo", cmdArgs: []string{"hello"}},
		{name: "URL", args: []string{"http://worker:8080", "--", "echo"}, worker: "http://worker:8080", command: "echo"},
		{name: "exclude", args: []string{"debian", "--exclude", "vendor", "--", "echo"}, worker: "debian", command: "echo", exclude: "vendor"},
		{name: "missing separator", args: []string{"debian", "echo", "hello"}, wantFail: true},
		{name: "missing command", args: []string{"debian", "--"}, wantFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker, command, cmdArgs, exclusions, err := parseRunArgs(test.args)
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
			if test.exclude != "" && !containsString(exclusions, test.exclude) {
				t.Fatalf("parseRunArgs() exclusions = %q, want %q", exclusions, test.exclude)
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
