package firecracker

import (
	"bytes"
	"fmt"
	"os"
)

const (
	sIfReg = 0o100000
	sIfDir = 0o040000
	sIfLnk = 0o120000
)

type initramfs struct {
	entries []cpioEntry
	nextIno uint64
}

type cpioEntry struct {
	ino  uint64
	name string
	mode uint32
	data []byte
}

func (b *initramfs) addDir(name string) { b.add(name, sIfDir|0o755, nil) }
func (b *initramfs) addFile(name string, mode uint32, data []byte) {
	b.add(name, sIfReg|mode, data)
}
func (b *initramfs) addSymlink(name, target string) {
	b.add(name, sIfLnk|0o777, []byte(target))
}

func (b *initramfs) add(name string, mode uint32, data []byte) {
	b.nextIno++
	b.entries = append(b.entries, cpioEntry{ino: b.nextIno, name: name, mode: mode, data: data})
}

// BuildInitramfs writes a cpio (newc) initramfs containing the static guest
// agent as /init, a static busybox, and applet symlinks.
func BuildInitramfs(agentPath, busyboxPath string, applets []string, outPath string) error {
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("read guest agent: %w", err)
	}
	busybox, err := os.ReadFile(busyboxPath)
	if err != nil {
		return fmt.Errorf("read busybox: %w", err)
	}
	image := &initramfs{}
	image.addDir("/")
	image.addDir("/bin")
	image.addFile("/init", 0o755, agent)
	image.addFile("/bin/busybox", 0o755, busybox)
	seen := map[string]bool{"busybox": true}
	for _, applet := range applets {
		if applet == "" || seen[applet] {
			continue
		}
		seen[applet] = true
		image.addSymlink("/bin/"+applet, "busybox")
	}
	image.addSymlink("/bin/sh", "busybox")
	if err := os.WriteFile(outPath, image.bytes(), 0o600); err != nil {
		return fmt.Errorf("write initramfs: %w", err)
	}
	return nil
}

func (b *initramfs) bytes() []byte {
	var out bytes.Buffer
	for _, entry := range b.entries {
		writeCpioEntry(&out, entry)
	}
	writeCpioEntry(&out, cpioEntry{name: "TRAILER!!!", mode: sIfDir})
	return out.Bytes()
}

func writeCpioEntry(out *bytes.Buffer, entry cpioEntry) {
	name := entry.name + "\x00"
	header := fmt.Sprintf("070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		entry.ino,
		entry.mode,
		0, // uid
		0, // gid
		1, // nlink
		0, // mtime
		len(entry.data),
		0, // devmajor
		0, // devminor
		0, // rdevmajor
		0, // rdevminor
		len(name),
		0, // check
	)
	out.WriteString(header)
	out.WriteString(name)
	padTo4(out, len(header)+len(name))
	out.Write(entry.data)
	padTo4(out, len(entry.data))
}

func padTo4(out *bytes.Buffer, size int) {
	if rem := size % 4; rem != 0 {
		out.Write(make([]byte, 4-rem))
	}
}
