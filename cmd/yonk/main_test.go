package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

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
		{name: "bad timeout", args: []string{"debian", "--timeout", "9999", "--", "echo"}, wantFail: true},
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

func TestWriteArtifactRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifact(dir, "out.txt", []byte("overwrite"), io.Discard); err == nil {
		t.Fatal("writeArtifact() followed a symlink")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}

func TestWriteArtifactWritesRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeArtifact(dir, "out.txt", []byte("data"), io.Discard); err != nil {
		t.Fatalf("writeArtifact() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "data" {
		t.Fatalf("content = %q", content)
	}
}

func TestArtifactBaseNames(t *testing.T) {
	names := artifactBaseNames([]string{"dist/app.js", "report.txt"})
	if !names["app.js"] || !names["report.txt"] {
		t.Fatalf("artifactBaseNames() = %v", names)
	}
	if names["other.txt"] {
		t.Fatalf("artifactBaseNames() contains an unexpected entry")
	}
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
