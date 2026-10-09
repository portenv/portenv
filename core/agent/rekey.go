// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// maxSecretsLine bounds one secrets line on stdin (a token, a certificate
// and a key take about 1.3 KiB).
const maxSecretsLine = 16 << 10

// channelKeys are the channel's current secrets. A re-key (ADR 0014: a new
// line on stdin, written by a restarted portenvd or runner) swaps the token
// and the certificate together, then closes every connection accepted
// before it, so the old secrets stop working at once.
type channelKeys struct {
	cur   atomic.Pointer[keySet]
	mu    sync.Mutex
	conns map[*trackedConn]uint64 // connection → generation at accept
}

type keySet struct {
	token []byte
	cert  *tls.Certificate
	gen   uint64
}

func newChannelKeys(sec ChannelSecrets) (*channelKeys, error) {
	ks, err := sec.keySet()
	if err != nil {
		return nil, err
	}
	k := &channelKeys{conns: map[*trackedConn]uint64{}}
	k.cur.Store(ks)
	return k, nil
}

func (s ChannelSecrets) keySet() (*keySet, error) {
	token := []byte(strings.TrimSpace(s.Token))
	if len(token) < 32 {
		return nil, errors.New("channel token is too short")
	}
	cert, err := tls.X509KeyPair(s.CertPEM, s.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load channel certificate: %w", err)
	}
	return &keySet{token: token, cert: &cert}, nil
}

// rekey makes sec the channel's secrets and closes connections made with
// the old ones.
func (k *channelKeys) rekey(sec ChannelSecrets) error {
	ks, err := sec.keySet()
	if err != nil {
		return err
	}
	k.mu.Lock()
	ks.gen = k.cur.Load().gen + 1
	k.cur.Store(ks)
	var old []*trackedConn
	for c, gen := range k.conns {
		if gen < ks.gen {
			old = append(old, c)
		}
	}
	k.mu.Unlock()
	for _, c := range old {
		_ = c.Close()
	}
	return nil
}

func (k *channelKeys) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return k.cur.Load().cert, nil
		},
		// No resumption: every connection presents the current certificate.
		SessionTicketsDisabled: true,
	}
}

func (k *channelKeys) token() []byte { return k.cur.Load().token }

// listen wraps lis so the channel knows which secrets each connection was
// accepted under.
func (k *channelKeys) listen(lis net.Listener) net.Listener {
	return &trackingListener{Listener: lis, k: k}
}

type trackingListener struct {
	net.Listener
	k *channelKeys
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, k: l.k}
	l.k.mu.Lock()
	l.k.conns[tc] = l.k.cur.Load().gen
	l.k.mu.Unlock()
	return tc, nil
}

type trackedConn struct {
	net.Conn
	k    *channelKeys
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.k.mu.Lock()
		delete(c.k.conns, c)
		c.k.mu.Unlock()
	})
	return c.Conn.Close()
}

// secretsLines reads secrets lines from stdin: strict JSON with known
// fields only, at most maxSecretsLine bytes. A longer line is dropped
// whole; a line cut short by the end of input is dropped too.
type secretsLines struct{ r *bufio.Reader }

func newSecretsLines(r io.Reader) *secretsLines {
	return &secretsLines{r: bufio.NewReaderSize(r, 4096)}
}

// next returns the next line's secrets. err is nil for a bad line (ok is
// false), and io.EOF when stdin is closed.
func (s *secretsLines) next() (sec ChannelSecrets, ok bool, err error) {
	var line []byte
	over := false
	for {
		chunk, err := s.r.ReadSlice('\n')
		if !over {
			line = append(line, chunk...)
			if len(line) > maxSecretsLine+1 {
				over, line = true, nil
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return ChannelSecrets{}, false, err
		}
		if over {
			return ChannelSecrets{}, false, nil
		}
		return parseSecretsLine(line)
	}
}

func parseSecretsLine(line []byte) (ChannelSecrets, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	dec.DisallowUnknownFields()
	var sec ChannelSecrets
	if err := dec.Decode(&sec); err != nil || dec.More() {
		return ChannelSecrets{}, false, nil
	}
	if _, err := sec.keySet(); err != nil {
		return ChannelSecrets{}, false, nil
	}
	return sec, true, nil
}

// serveStdinChannel serves the agent API with the secrets of stdin's first
// line, then applies every later valid line as a re-key (ADR 0014). Bad
// lines are ignored and never logged. It listens only once it has the
// first secrets: init waits for the port as the sign that they were read
// (ADR 0010). When stdin closes, it keeps serving with the secrets it has.
func serveStdinChannel(ctx context.Context, cfg Config, stdin io.Reader, listen func() (net.Listener, error), log *slog.Logger) error {
	lines := newSecretsLines(stdin)
	var first ChannelSecrets
	for {
		sec, ok, err := lines.next()
		if err != nil {
			return fmt.Errorf("read channel secrets: %w", err)
		}
		if ok {
			first = sec
			break
		}
		log.Warn("ignored a malformed agent channel line")
	}
	keys, err := newChannelKeys(first)
	if err != nil {
		return err
	}
	lis, err := listen()
	if err != nil {
		return err
	}
	go func() {
		for {
			sec, ok, err := lines.next()
			if err != nil {
				return
			}
			if !ok {
				log.Warn("ignored a malformed agent channel line")
				continue
			}
			if err := keys.rekey(sec); err != nil {
				log.Warn("ignored an unusable agent channel line")
				continue
			}
			log.Info("agent channel re-keyed")
		}
	}()
	return serveOn(ctx, newChannelServer(cfg, keys), keys.listen(lis))
}
