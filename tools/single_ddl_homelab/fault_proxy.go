package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type proxyEvidence struct {
	Mode       string     `json:"mode"`
	DDLCalls   int64      `json:"forwardedDdlPackets"`
	SetupCalls int64      `json:"timeoutSets"`
	Sessions   []int      `json:"ddlPhysicalConnections"`
	Timeouts   []string   `json:"timeoutStatements"`
	Readbacks  [][]string `json:"numericSessionReadbacks"`
}
type proxy struct {
	listener      net.Listener
	mode, fixture string
	mu            sync.Mutex
	conns         map[net.Conn]struct{}
	count         atomic.Int64
	setup         atomic.Int64
	serial        atomic.Int64
	sessions      []int
	timeouts      []string
	readbacks     [][]string
	wg            sync.WaitGroup
}

func startProxy(mode, fixture string) (*proxy, error) {
	l, err := net.Listen("tcp", "10.136.38.51:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{listener: l, mode: mode, fixture: fixture, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.forward(c) }()
		}
	}()
	return p, nil
}
func (p *proxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }
func (p *proxy) close() {
	_ = p.listener.Close()
	p.mu.Lock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
func (p *proxy) snapshot() proxyEvidence {
	p.mu.Lock()
	defer p.mu.Unlock()
	return proxyEvidence{p.mode, p.count.Load(), p.setup.Load(), append([]int(nil), p.sessions...), append([]string(nil), p.timeouts...), append([][]string(nil), p.readbacks...)}
}
func packet(c net.Conn) ([]byte, []byte, error) {
	h := make([]byte, 4)
	_, err := io.ReadFull(c, h)
	if err != nil {
		return nil, nil, err
	}
	n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
	b := make([]byte, n)
	_, err = io.ReadFull(c, b)
	return h, b, err
}
func numericCells(b []byte) []string {
	var result []string
	for len(b) > 0 {
		n := int(b[0])
		b = b[1:]
		if n == 0 || n > 30 || len(b) < n {
			return nil
		}
		s := string(b[:n])
		if _, err := strconv.ParseUint(s, 10, 64); err != nil {
			return nil
		}
		result = append(result, s)
		b = b[n:]
	}
	if len(result) < 3 {
		return nil
	}
	return result
}
func (p *proxy) forward(down net.Conn) {
	defer down.Close()
	up, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	id := int(p.serial.Add(1))
	p.mu.Lock()
	p.conns[down] = struct{}{}
	p.conns[up] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.conns, down); delete(p.conns, up); p.mu.Unlock() }()
	var readback atomic.Bool
	var discardResponse atomic.Bool
	// Response forwarding is packet based; only numeric session-state cells are retained.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			h, b, e := packet(up)
			if e != nil {
				_ = down.Close()
				return
			}
			if discardResponse.Load() {
				// Consume the actual DDL response before dropping downstream; no claim of its outcome reaches the caller.
				_ = down.Close()
				return
			}
			if readback.Load() {
				if cells := numericCells(b); cells != nil {
					p.mu.Lock()
					p.readbacks = append(p.readbacks, cells)
					p.mu.Unlock()
				}
			}
			if _, e = io.Copy(down, bytes.NewReader(append(h, b...))); e != nil {
				_ = up.Close()
				return
			}
		}
	}()
	defer func() { _ = up.Close(); _ = down.Close(); <-done }()
	for {
		h, b, err := packet(down)
		if err != nil {
			return
		}
		if len(b) > 0 && b[0] == 3 {
			q := strings.ToLower(strings.TrimSpace(string(b[1:])))
			readback.Store(strings.HasPrefix(q, "select ") && strings.Contains(q, "connection_id()") && strings.Contains(q, "ob_query_timeout"))
			ddl := (strings.HasPrefix(q, "create table ") || strings.HasPrefix(q, "create index ") || strings.HasPrefix(q, "alter table ") || strings.HasPrefix(q, "drop table ") || strings.HasPrefix(q, "drop index ")) && strings.Contains(q, p.fixture)
			setup := strings.HasPrefix(q, "set session ") && strings.Contains(q, "ob_query_timeout")
			if setup {
				p.setup.Add(1)
				p.mu.Lock()
				p.timeouts = append(p.timeouts, q)
				p.mu.Unlock()
			}
			if p.mode == "reject-set" && setup {
				msg := []byte{255, 0, 0, '#', 'H', 'Y', '0', '0', '0'}
				binary.LittleEndian.PutUint16(msg[1:3], 1231)
				msg = append(msg, []byte("Injected homelab timeout SET rejection")...)
				reply := []byte{byte(len(msg)), byte(len(msg) >> 8), byte(len(msg) >> 16), h[3] + 1}
				if _, err = io.Copy(down, bytes.NewReader(append(reply, msg...))); err != nil {
					return
				}
				continue
			}
			if ddl {
				p.count.Add(1)
				p.mu.Lock()
				p.sessions = append(p.sessions, id)
				p.mu.Unlock()
				if p.mode == "lose-ddl-response" {
					discardResponse.Store(true)
				}
			}
		}
		if _, err = io.Copy(up, bytes.NewReader(append(h, b...))); err != nil {
			return
		}
	}
}
