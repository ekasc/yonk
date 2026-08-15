// Command fake-firecracker simulates Firecracker and the guest agent for
// executor tests. It serves the Unix socket API (accepting every PUT) and,
// once started, connects to the guest-initiated vsock socket and speaks the
// guest protocol.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ekasc/yonk/internal/guestproto"
)

type vsockBody struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

func main() {
	var apiSocket string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--api-sock" && i+1 < len(args) {
			apiSocket = args[i+1]
		}
	}
	if apiSocket == "" {
		fmt.Fprintln(os.Stderr, "fake-firecracker: missing --api-sock")
		os.Exit(1)
	}

	listener, err := net.Listen("unix", apiSocket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-firecracker: listen: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	vsockCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/vsock" {
			body, _ := io.ReadAll(r.Body)
			var vsock vsockBody
			if err := json.Unmarshal(body, &vsock); err == nil {
				select {
				case vsockCh <- vsock.UDSPath:
				default:
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()

	vsockPath := <-vsockCh
	addr := fmt.Sprintf("%s_%d", vsockPath, guestproto.HostPort)
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
	runGuest(conn)
}

func runGuest(conn net.Conn) {
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
