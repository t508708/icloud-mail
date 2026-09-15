package mail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type idlePeer struct {
	conn     net.Conn
	events   chan string
	commands chan string
}

func newIdlePeer(t *testing.T, mode string) (net.Conn, *idlePeer) {
	t.Helper()
	client, server := net.Pipe()
	p := &idlePeer{conn: server, events: make(chan string, 50), commands: make(chan string, 50)}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		defer server.Close()
		caps := "IMAP4rev1"
		if mode != "unsupported" {
			caps += " IDLE"
		}
		_, _ = fmt.Fprintf(server, "* OK [CAPABILITY %s] ready\r\n", caps)
		lines := make(chan string)
		closed := make(chan struct{})
		defer close(closed)
		go func() {
			defer close(lines)
			s := bufio.NewScanner(server)
			for s.Scan() {
				select {
				case lines <- s.Text():
				case <-closed:
					return
				}
			}
		}()
		for {
			select {
			case event := <-p.events:
				if _, err := fmt.Fprint(server, event); err != nil {
					return
				}
			case line, ok := <-lines:
				if !ok {
					return
				}
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				tag, command := fields[0], strings.ToUpper(fields[1])
				p.commands <- command
				switch command {
				case "CAPABILITY":
					_, _ = fmt.Fprintf(server, "* CAPABILITY %s\r\n%s OK capability\r\n", caps, tag)
				case "LOGIN":
					if mode != "stall-login" {
						_, _ = fmt.Fprintf(server, "%s OK [CAPABILITY %s] login\r\n", tag, caps)
					}
				case "EXAMINE", "SELECT":
					_, _ = fmt.Fprintf(server, "* 0 EXISTS\r\n%s OK [READ-ONLY] select\r\n", tag)
				case "IDLE":
					if mode != "stall-idle" {
						_, _ = fmt.Fprint(server, "+ idling\r\n")
					}
				default:
					_, _ = fmt.Fprintf(server, "%s BAD unexpected command\r\n", tag)
				}
			}
		}
	}()
	return client, p
}

func idleReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher")
		var zero T
		return zero
	}
}

func TestWatchIMAPConnectionNotifiesSeriallyAndStaysQuiet(t *testing.T) {
	conn, peer := newIdlePeer(t, "normal")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notified := make(chan struct{}, 100)
	var inCallback, maxCallbacks atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- watchIMAPConnection(ctx, conn, "user", "pass", time.Second, func() {
			n := inCallback.Add(1)
			if n > maxCallbacks.Load() {
				maxCallbacks.Store(n)
			}
			time.Sleep(time.Millisecond)
			inCallback.Add(-1)
			notified <- struct{}{}
		})
	}()
	idleReceive(t, notified)
	select {
	case <-notified:
		t.Fatal("idle mailbox generated another notification")
	case <-time.After(30 * time.Millisecond):
	}
	peer.events <- "* 1 EXISTS\r\n"
	idleReceive(t, notified)
	for i := 2; i < 30; i++ {
		peer.events <- fmt.Sprintf("* %d EXISTS\r\n", i)
	}
	idleReceive(t, notified)
	cancel()
	if err := idleReceive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if maxCallbacks.Load() != 1 {
		t.Fatalf("concurrent callbacks = %d", maxCallbacks.Load())
	}
	for len(peer.commands) > 0 {
		cmd := <-peer.commands
		if cmd == "FETCH" || cmd == "SEARCH" || cmd == "NOOP" {
			t.Fatalf("notification connection issued %s", cmd)
		}
	}
}

func TestWatchIMAPConnectionUnsupportedAndSetupTimeout(t *testing.T) {
	for _, mode := range []string{"unsupported", "stall-login", "stall-idle"} {
		t.Run(mode, func(t *testing.T) {
			conn, _ := newIdlePeer(t, mode)
			var calls atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- watchIMAPConnection(context.Background(), conn, "user", "pass", 80*time.Millisecond, func() { calls.Add(1) })
			}()
			err := idleReceive(t, done)
			want := context.DeadlineExceeded
			if mode == "unsupported" {
				want = ErrIdleUnsupported
			}
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if calls.Load() != 0 {
				t.Fatal("notification before IDLE acknowledgement")
			}
		})
	}
}

func TestWatchIMAPConnectionDisconnectReturns(t *testing.T) {
	conn, peer := newIdlePeer(t, "normal")
	notified := make(chan struct{}, 2)
	done := make(chan error, 1)
	go func() {
		done <- watchIMAPConnection(context.Background(), conn, "user", "pass", time.Second, func() { notified <- struct{}{} })
	}()
	idleReceive(t, notified)
	_ = peer.conn.Close()
	if err := idleReceive(t, done); err == nil {
		t.Fatal("disconnect returned success")
	}
}
