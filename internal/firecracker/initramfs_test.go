package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildInitramfs(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "agent")
	busyboxPath := filepath.Join(dir, "busybox")
	if err := os.WriteFile(agentPath, []byte("agent-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(busyboxPath, []byte("busybox-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "initramfs.cpio")
	if err := BuildInitramfs(agentPath, busyboxPath, []string{"echo", "sh", "ls"}, out); err != nil {
		t.Fatalf("BuildInitramfs() error = %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	entries := readCpio(t, data)

	if got := entries["/init"].mode; got != 0o100755 {
		t.Fatalf("/init mode = %#o, want 0100755", got)
	}
	if string(entries["/init"].data) != "agent-bytes" {
		t.Fatalf("/init data = %q", entries["/init"].data)
	}
	if string(entries["/bin/busybox"].data) != "busybox-bytes" {
		t.Fatalf("/bin/busybox data = %q", entries["/bin/busybox"].data)
	}
	if got := entries["/bin/sh"].data; string(got) != "busybox" {
		t.Fatalf("/bin/sh target = %q, want busybox", got)
	}
	if _, ok := entries["/bin/echo"]; !ok {
		t.Fatal("missing /bin/echo applet symlink")
	}
	if _, ok := entries["TRAILER!!!"]; !ok {
		t.Fatal("missing cpio trailer")
	}
}

type cpioInfo struct {
	mode uint32
	data []byte
}

func readCpio(t *testing.T, data []byte) map[string]cpioInfo {
	t.Helper()
	entries := map[string]cpioInfo{}
	for offset := 0; offset < len(data); {
		if offset+110 > len(data) {
			t.Fatalf("truncated cpio header at %d", offset)
		}
		header := data[offset : offset+110]
		if string(header[0:6]) != "070701" {
			t.Fatalf("bad cpio magic at %d: %q", offset, header[0:6])
		}
		fields := [13]uint64{}
		for i := 0; i < 13; i++ {
			fields[i] = parseHex(t, string(header[6+8*i:6+8*i+8]))
		}
		nameSize := int(fields[11])
		fileSize := int(fields[6])
		offset += 110
		nameBytes := data[offset : offset+nameSize]
		name := strings.TrimRight(string(nameBytes), "\x00")
		offset += alignFrom(offset, nameSize)
		fileData := data[offset : offset+fileSize]
		offset += alignFrom(offset, fileSize)
		entries[name] = cpioInfo{mode: uint32(fields[1]), data: fileData}
	}
	return entries
}

func parseHex(t *testing.T, s string) uint64 {
	t.Helper()
	var result uint64
	for _, c := range s {
		var digit uint64
		switch {
		case c >= '0' && c <= '9':
			digit = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			digit = uint64(c-'a') + 10
		default:
			t.Fatalf("bad hex %q", s)
		}
		result = result<<4 | digit
	}
	return result
}

// alignFrom returns the padding needed after size bytes starting at offset
// so that the following field is 4-byte aligned from the entry start.
func alignFrom(offset, size int) int {
	end := offset + size
	if rem := end % 4; rem != 0 {
		return size + 4 - rem
	}
	return size
}
