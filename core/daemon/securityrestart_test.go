// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/portenv/portenv/core/driver"
	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// fakeTakeOver records what the take-over asked of the driver, in order.
type fakeTakeOver struct {
	channel error // AgentChannel before anything (nil: this process has one)
	rekey   error
	calls   []string
	keyed   bool
}

func (f *fakeTakeOver) AgentChannel(context.Context, driver.BoxID) (driver.AgentChannel, error) {
	f.calls = append(f.calls, "channel")
	if f.channel == nil || f.keyed {
		return driver.AgentChannel{}, nil
	}
	return driver.AgentChannel{}, f.channel
}

func (f *fakeTakeOver) Rekey(context.Context, driver.BoxID) error {
	f.calls = append(f.calls, "rekey")
	if f.rekey == nil {
		f.keyed = true
	}
	return f.rekey
}

func (f *fakeTakeOver) Stop(context.Context, driver.BoxID, time.Duration) (driver.State, error) {
	f.calls = append(f.calls, "stop")
	return driver.StateStopped, nil
}

// TestATakeOverOfAnOldMountRestartsTheWorkspace (R-0201, ruling 1): a box
// left running with the old mount is stopped (then started again by the
// open), and the open records that it did so for a security update. It's
// logged by workspace ID only, with no path or name.
func TestATakeOverOfAnOldMountRestartsTheWorkspace(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, c := range []struct {
		name      string
		f         *fakeTakeOver
		restarted bool
		calls     string
	}{
		{"its own channel already", &fakeTakeOver{}, false, "channel"},
		{"re-keyed in place", &fakeTakeOver{channel: driver.ErrNoChannel}, false, "channel rekey channel"},
		{"the old mount", &fakeTakeOver{channel: driver.ErrNoChannel, rekey: driver.ErrMountsChanged}, true, "channel rekey channel stop"},
		{"not running, or another failure", &fakeTakeOver{channel: driver.ErrNoChannel, rekey: errors.New("not running")}, false, "channel rekey channel stop"},
	} {
		logs.Reset()
		restarted, err := s.takeOver(context.Background(), "acme-api", c.f, "box-0123456789ab")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if restarted != c.restarted || strings.Join(c.f.calls, " ") != c.calls {
			t.Fatalf("%s: restarted %v, calls %q; want %v, %q", c.name, restarted, strings.Join(c.f.calls, " "), c.restarted, c.calls)
		}
		if c.restarted {
			l := logs.String()
			if !strings.Contains(l, "security update") || !strings.Contains(l, "box-0123456789ab") {
				t.Fatalf("%s: logged %q, want the restart by workspace ID", c.name, l)
			}
			if strings.Contains(l, "acme-api") || strings.Contains(l, "/") {
				t.Fatalf("%s: logged %q, which names the workspace or a path", c.name, l)
			}
		}
	}
}

// TestTheSecurityRestartIsReportedForThatOpenOnly (R-0201): the state says
// the workspace was restarted for a security update only for the open
// that did it; a later open of the same workspace doesn't.
func TestTheSecurityRestartIsReportedForThatOpenOnly(t *testing.T) {
	restarted := &openBox{}
	restarted.securityRestart.Store(true)
	r := &daemonv1.GetBoxStateResponse{}
	addOpenNotices(r, restarted)
	if !r.GetRestartedForSecurityUpdate() {
		t.Fatal("the open that restarted the workspace doesn't say so")
	}
	r = &daemonv1.GetBoxStateResponse{}
	addOpenNotices(r, &openBox{})
	if r.GetRestartedForSecurityUpdate() {
		t.Fatal("a later open says it restarted the workspace")
	}
}

// TestAStreamGetsTheBoxStoppingEnd (R-0201): when portenvd stops a box it
// has open (a close, a move, a restart, the security restart), a tab
// terminal on it ends with BOX_STOPPING rather than just breaking, so an
// agent's stream never hangs or reads it as a failure.
func TestAStreamGetsTheBoxStoppingEnd(t *testing.T) {
	hangUp := make(chan struct{})
	helper := &fakeTabHelper{boxID: "box-a", script: resetFirst(), hangUp: hangUp}
	var srv *Server
	w := servingTabTerminalsWith(t, ownerCaller(), helper, &fakeTabHelper{boxID: "box-b"}, func(s *Server) { srv = s })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openTab(ctx, t, w.c, &daemonv1.TabTerminalOpen{Box: "acme-api", Session: "main", Tab: "@0"})
	if _, err := st.Recv(); err != nil {
		t.Fatal(err)
	}
	srv.leaving("acme-api") // portenvd is stopping it
	close(hangUp)           // and the helper's stream goes with the box
	r, err := st.Recv()
	if err != nil {
		t.Fatalf("the stream broke (%v), want the box-stopping end", err)
	}
	if r.GetEnd() != typesv1.TabTerminalEnd_TAB_TERMINAL_END_BOX_STOPPING {
		t.Fatalf("got %v, want end BOX_STOPPING", r)
	}
	if _, err := st.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the end: %v, want nothing more", err)
	}
}
