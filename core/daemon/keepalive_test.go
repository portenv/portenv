// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// TestClientKeepalivePingsAreAccepted (R-0294): the app pings portenvd to
// notice it frozen. Pings keepaliveMinTime apart, with no call open, are
// each answered and never earn a GOAWAY; gRPC's default policy closes the
// connection on the third such ping.
func TestClientKeepalivePingsAreAccepted(t *testing.T) {
	dir, err := os.MkdirTemp("", "pk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(dir, "ka.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := (&Server{}).grpcServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(http2.ClientPreface)); err != nil {
		t.Fatal(err)
	}
	// Reads and writes are separate framers: the reader acks the server's
	// settings while the test pings.
	rd := http2.NewFramer(nil, conn)
	var wmu sync.Mutex
	wr := http2.NewFramer(conn, nil)
	write := func(f func(*http2.Framer) error) error {
		wmu.Lock()
		defer wmu.Unlock()
		return f(wr)
	}
	if err := write(func(f *http2.Framer) error { return f.WriteSettings() }); err != nil {
		t.Fatal(err)
	}

	acks := make(chan [8]byte, 16)
	goaway := make(chan http2.ErrCode, 1)
	go func() {
		for {
			f, err := rd.ReadFrame()
			if err != nil {
				return
			}
			switch f := f.(type) {
			case *http2.SettingsFrame:
				if !f.IsAck() {
					_ = write(func(f *http2.Framer) error { return f.WriteSettingsAck() })
				}
			case *http2.PingFrame:
				if f.IsAck() {
					acks <- f.Data
				}
			case *http2.GoAwayFrame:
				goaway <- f.ErrCode
				return
			}
		}
	}()

	const pings = 4 // the default policy's third strike is the fourth ping
	for i := range pings {
		if i > 0 {
			time.Sleep(keepaliveMinTime + 200*time.Millisecond)
		}
		if err := write(func(f *http2.Framer) error { return f.WritePing(false, [8]byte{byte(i + 1)}) }); err != nil {
			t.Fatalf("ping %d: %v", i+1, err)
		}
		select {
		case d := <-acks:
			if d[0] != byte(i+1) {
				t.Fatalf("ping %d answered as %d", i+1, d[0])
			}
		case code := <-goaway:
			t.Fatalf("ping %d: GOAWAY %v; the app's keepalive would close every connection", i+1, code)
		case <-time.After(3 * time.Second):
			t.Fatalf("ping %d: no answer", i+1)
		}
	}
	select {
	case code := <-goaway:
		t.Fatalf("GOAWAY %v after the pings", code)
	case <-time.After(300 * time.Millisecond):
	}
}
