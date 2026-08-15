// Command fake-firecracker simulates Firecracker and the guest agent for
// executor tests. It parses the same --config-file flag, connects to the
// guest-initiated vsock socket, and speaks the guest protocol.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/guestproto"
)

type config struct {
	VSock struct {
		GuestCID uint32 `json:"guest_cid"`
		UDSPath  string `json:"uds_path"`
	} `json:"vsock"`
}

func main() {
	var cfgPath string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--config-file" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "fake-firecracker: missing --config-file")
		os.Exit(1)
	}
	var cfg config
	if data, err := os.ReadFile(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "fake-firecracker: read config: %v\n", err)
		os.Exit(1)
	} else if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "fake-firecracker: parse config: %v\n", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf("%s_%d", cfg.VSock.UDSPath, guestproto.HostPort)
	var conn net.Conn
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := net.Dial("unix", addr)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "fake-firecracker: could not connect to %s\n", addr)
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(guestproto.Message{Type: guestproto.MsgHello}); err != nil {
		os.Exit(1)
	}
	for {
		var msg guestproto.Message
		if err := dec.Decode(&msg); err != nil {
			os.Exit(1)
		}
		switch msg.Type {
		case guestproto.MsgJob:
			if msg.Job == nil {
				os.Exit(1)
			}
			switch msg.Job.Command {
			case "sleep":
				for {
					var m guestproto.Message
					if err := dec.Decode(&m); err != nil {
						os.Exit(1)
					}
					if m.Type == guestproto.MsgCancel {
						_ = enc.Encode(guestproto.Message{Type: guestproto.MsgResult, Result: &guestproto.Result{ExitCode: 130, TerminationReason: "cancelled"}})
						return
					}
				}
			case "crash":
				return // die without a result
			default:
				_ = enc.Encode(guestproto.Message{Type: guestproto.MsgStdout, Data: []byte(strings.Join(msg.Job.Args, " ") + "\n")})
				_ = enc.Encode(guestproto.Message{Type: guestproto.MsgResult, Result: &guestproto.Result{ExitCode: 0, TerminationReason: "completed"}})
				return
			}
		}
	}
}
