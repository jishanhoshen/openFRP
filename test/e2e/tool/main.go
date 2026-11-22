// Command e2e-tool provides the two ends of the end-to-end test:
//
//	e2e-tool echo  -tcp 127.0.0.1:7777 -udp 127.0.0.1:7778
//	    a stand-in "game server" on the agent side (TCP + UDP echo)
//	e2e-tool scenario -api http://control:8080 -relay frps -agent-config /data/agent.yaml
//	    drives the full Phase 3 flow (register, pair, allocate, traffic, revoke)
//	    while acting as both the account owner and the external "player".
package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: e2e-tool <echo|scenario> [flags]")
	}
	switch os.Args[1] {
	case "echo":
		runEcho(os.Args[2:])
	case "scenario":
		os.Exit(runScenario(os.Args[2:]))
	default:
		log.Fatalf("unknown mode %q", os.Args[1])
	}
}

func runEcho(args []string) {
	fs := flag.NewFlagSet("echo", flag.ExitOnError)
	tcpAddr := fs.String("tcp", "127.0.0.1:7777", "")
	udpAddr := fs.String("udp", "127.0.0.1:7778", "")
	_ = fs.Parse(args)

	ln, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	pc, err := net.ListenPacket("udp", *udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("echo listening tcp=%s udp=%s", *tcpAddr, *udpAddr)
	buf := make([]byte, 2048)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		_, _ = pc.WriteTo(buf[:n], from)
	}
}

func payload() []byte {
	b := make([]byte, 512)
	_, _ = rand.Read(b)
	return b
}

func tcpRoundTrip(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	p := payload()
	if _, err := c.Write(p); err != nil {
		return err
	}
	got := make([]byte, len(p))
	if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if !bytes.Equal(p, got) {
		return fmt.Errorf("payload mismatch")
	}
	return nil
}

func udpRoundTrip(addr string) error {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	p := payload()
	if _, err := c.Write(p); err != nil {
		return err
	}
	got := make([]byte, 2048)
	n, err := c.Read(got)
	if err != nil {
		return err
	}
	if !bytes.Equal(p, got[:n]) {
		return fmt.Errorf("payload mismatch")
	}
	return nil
}

func retry(name string, deadline time.Time, f func() error) error {
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			log.Printf("PASS %s", name)
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("FAIL %s: %w", name, err)
}
