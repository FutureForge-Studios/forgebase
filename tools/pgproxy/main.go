// pgproxy - a Postgres-protocol-aware TCP proxy that makes scale-to-zero
// invisible. It reads the connection's startup packet to learn the target
// database, cold-starts that project's instance if it is stopped (via
// pg-instance.sh), then transparently splices the client to the backend. A
// client never notices its project was suspended - it just pays a ~sub-second
// wake on the first connection.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
)

const sslRequest = 80877103
const gssRequest = 80877104

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

var instScript = envOr("PGINSTANCE", "/opt/pgforge/bin/pg-instance.sh")

func main() {
	listen := envOr("PGPROXY_LISTEN", "127.0.0.1:7000")
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("listen %s: %v", listen, err)
	}
	log.Printf("pgproxy listening on %s (cold-start via %s)", listen, instScript)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(c)
	}
}

func handle(client net.Conn) {
	defer client.Close()
	startup, db, err := readStartup(client)
	if err != nil {
		log.Printf("startup read: %v", err)
		return
	}
	if db == "" {
		log.Printf("no database in startup packet")
		return
	}
	// database name maps to the project/branch instance (identity mapping).
	port, err := coldStart(db)
	if err != nil {
		log.Printf("cold-start %q: %v", db, err)
		return
	}
	backend, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		log.Printf("dial backend %s: %v", port, err)
		return
	}
	defer backend.Close()
	// replay the startup packet we consumed, then splice both directions
	if _, err := backend.Write(startup); err != nil {
		return
	}
	go io.Copy(backend, client)
	io.Copy(client, backend)
}

// readStartup consumes the client's startup phase, answering SSL/GSS requests
// with "not supported" (the instances speak plaintext on loopback), and returns
// the raw StartupMessage bytes plus the requested database.
func readStartup(c net.Conn) (raw []byte, db string, err error) {
	for {
		hdr := make([]byte, 4)
		if _, err = io.ReadFull(c, hdr); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint32(hdr))
		if length < 8 || length > 1<<20 {
			return nil, "", fmt.Errorf("bad startup length %d", length)
		}
		body := make([]byte, length-4)
		if _, err = io.ReadFull(c, body); err != nil {
			return
		}
		code := binary.BigEndian.Uint32(body[:4])
		if length == 8 && (code == sslRequest || code == gssRequest) {
			if _, err = c.Write([]byte{'N'}); err != nil { // deny SSL/GSS
				return
			}
			continue
		}
		// StartupMessage: [int32 protocol][key\0value\0...\0]
		params := strings.Split(string(body[4:]), "\x00")
		for i := 0; i+1 < len(params); i += 2 {
			if params[i] == "database" {
				db = params[i+1]
			}
		}
		return append(hdr, body...), db, nil
	}
}

func coldStart(slug string) (string, error) {
	if out, err := exec.Command(instScript, "start", slug).CombinedOutput(); err != nil {
		return "", fmt.Errorf("start: %s", strings.TrimSpace(string(out)))
	}
	p, err := exec.Command(instScript, "port", slug).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(p)), nil
}
