package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"sync"
)

// IPCMessage is sent from CLI commands to the network monitor.
type IPCMessage struct {
	Session string   `json:"session"`
	Type    string   `json:"type"`
	Cmd     string   `json:"cmd,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// ipcServer listens on a unix domain socket for IPCMessages.
type ipcServer struct {
	listener net.Listener
	wg       sync.WaitGroup
	done     chan struct{}
}

func newIPCServer(sockPath string, handler func(IPCMessage)) (*ipcServer, error) {
	os.Remove(sockPath) // clean up any stale socket
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	srv := &ipcServer{listener: ln, done: make(chan struct{})}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-srv.done:
					return
				default:
					continue
				}
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					var msg IPCMessage
					if err := json.Unmarshal(scanner.Bytes(), &msg); err == nil {
						handler(msg)
					}
				}
			}(conn)
		}
	}()
	return srv, nil
}

func (s *ipcServer) close() {
	close(s.done)
	s.listener.Close()
	s.wg.Wait()
}

// ipcSend sends a single message to the monitor's unix socket (fire-and-forget).
func ipcSend(sockPath string, msg IPCMessage) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}
