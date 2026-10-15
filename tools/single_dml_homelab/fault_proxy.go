package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// This proxy is confined to dba-app and the homelab OB endpoint. It never logs authentication packets.
type faultProxy struct {
	listener  net.Listener
	upstream  string
	mode      string
	fixture   string
	mutations atomic.Int64
	setErrors atomic.Int64
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
}

func startFaultProxy(mode, fixture string) (*faultProxy, error) {
	listener, err := net.Listen("tcp", "10.136.38.51:0")
	if err != nil {
		return nil, err
	}
	p := &faultProxy{
		listener: listener,
		upstream: "10.136.38.71:2883",
		mode:     mode,
		fixture:  fixture,
		conns:    make(map[net.Conn]struct{}),
	}
	go p.accept()
	return p, nil
}

func (p *faultProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func (p *faultProxy) close() {
	_ = p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.conns {
		_ = conn.Close()
	}
}

func (p *faultProxy) accept() {
	for {
		downstream, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.forward(downstream)
	}
}

func (p *faultProxy) forward(downstream net.Conn) {
	defer downstream.Close()
	upstream, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer upstream.Close()
	p.mu.Lock()
	p.conns[downstream] = struct{}{}
	p.conns[upstream] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, downstream)
		delete(p.conns, upstream)
		p.mu.Unlock()
	}()
	go func() {
		_, _ = io.Copy(downstream, upstream)
		_ = downstream.Close()
	}()
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(downstream, header); err != nil {
			return
		}
		size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		payload := make([]byte, size)
		if _, err := io.ReadFull(downstream, payload); err != nil {
			return
		}
		if len(payload) > 0 && payload[0] == 3 { // COM_QUERY
			statement := strings.ToLower(strings.TrimSpace(string(payload[1:])))
			mutation := (strings.HasPrefix(statement, "update ") || strings.HasPrefix(statement, "insert ") || strings.HasPrefix(statement, "delete ")) && strings.Contains(statement, p.fixture)
			if mutation {
				p.mutations.Add(1)
			}
			if p.mode == "reject-set" && strings.HasPrefix(statement, "set session ") && strings.Contains(statement, "ob_query_timeout") {
				p.setErrors.Add(1)
				errorPayload := []byte{0xff, 0, 0, '#', 'H', 'Y', '0', '0', '0'}
				binary.LittleEndian.PutUint16(errorPayload[1:3], 1231)
				errorPayload = append(errorPayload, []byte("Injected homelab session timeout rejection")...)
				response := []byte{byte(len(errorPayload)), byte(len(errorPayload) >> 8), byte(len(errorPayload) >> 16), header[3] + 1}
				response = append(response, errorPayload...)
				if _, err := io.Copy(downstream, bytes.NewReader(response)); err != nil {
					return
				}
				continue
			}
			if p.mode == "drop-mutation-response" && mutation {
				packet := append(header, payload...)
				if _, err := io.Copy(upstream, bytes.NewReader(packet)); err != nil {
					return
				}
				// The SQL was forwarded. Neither the caller nor the proxy claims whether it committed.
				return
			}
		}
		if _, err := io.Copy(upstream, bytes.NewReader(append(header, payload...))); err != nil {
			return
		}
	}
}

func (p *faultProxy) summary() string {
	return fmt.Sprintf("mode=%s forwardedMutationPackets=%d rejectedTimeoutSets=%d", p.mode, p.mutations.Load(), p.setErrors.Load())
}
