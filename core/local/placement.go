// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portenv/portenv/core/internal/bounded"
)

// PingDeadline bounds asking a server whether it answers (R-0140): the
// lease sheet never waits longer for it. A variable only so tests on a
// loaded machine can give the answering case room.
var PingDeadline = 2 * time.Second

// machinesFile maps this Mac's servers (user@host) to their machine IDs,
// which a save's lease names. Not secret.
func (e *Env) machinesFile() string { return filepath.Join(e.Dir, "state", "server-machines.json") }

var machinesMu sync.Mutex

func (e *Env) knownMachines() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(e.machinesFile()) // #nosec G304 -- a fixed name in the Portenv directory
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// RememberServerMachine records host's machine ID.
func (e *Env) RememberServerMachine(host, machine string) {
	if host == "" || machine == "" {
		return
	}
	machinesMu.Lock()
	defer machinesMu.Unlock()
	m := e.knownMachines()
	if m[host] == machine {
		return
	}
	m[host] = machine
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = writeAtomic(e.machinesFile(), append(b, '\n'))
	}
}

// MachineIDOn asks host for its machine ID (`portenv machine-id`), within
// PingDeadline.
func MachineIDOn(ctx context.Context, host string) (string, error) {
	cmd, err := Remote(ctx, PingDeadline, host, PortenvPath, "machine-id")
	if err != nil {
		return "", err
	}
	cmd.Stdin = nil
	hardStop(cmd)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// ServerForMachine finds which of servers is the machine a lease names:
// remembered answers first, then each server not yet known is asked once,
// at the same time.
func (e *Env) ServerForMachine(ctx context.Context, servers []string, machine string) (string, bool) {
	machinesMu.Lock()
	known := e.knownMachines()
	machinesMu.Unlock()
	for host, id := range known {
		if id == machine {
			return host, true
		}
	}
	type answer struct{ host, id string }
	answers := make(chan answer, len(servers))
	asked := 0
	for _, host := range servers {
		if _, ok := known[host]; ok {
			continue
		}
		asked++
		go func() {
			id, err := MachineIDOn(ctx, host)
			if err != nil {
				id = ""
			}
			answers <- answer{host, id}
		}()
	}
	found := ""
	for range asked {
		a := <-answers
		if a.id == "" {
			continue
		}
		e.RememberServerMachine(a.host, a.id)
		if a.id == machine {
			found = a.host
		}
	}
	return found, found != ""
}

// ServerAnswers reports whether host's portenvd answers (`portenv app
// ping` over SSH) within PingDeadline.
func ServerAnswers(ctx context.Context, host string) bool {
	cmd, err := Remote(ctx, PingDeadline, host, "sudo", PortenvPath, "app", "ping")
	if err != nil {
		return false
	}
	cmd.Stdin = nil
	hardStop(cmd)
	return cmd.Run() == nil
}

// hardStop makes a probe end at its deadline: killed rather than asked,
// and not waited for (a ping or an ID can be asked again; the shared SSH
// connection, ControlMaster, is a separate process and isn't touched).
func hardStop(cmd *bounded.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 100 * time.Millisecond
}

// ServerID is a stable ID for one of this Mac's servers that doesn't carry
// its address: what the app is told, and gives back, instead of user@host.
func ServerID(target string) string {
	sum := sha256.Sum256([]byte("portenv server " + target))
	return "srv-" + hex.EncodeToString(sum[:6])
}

// ServerByID is the server (user@host) with this ID, among servers.
func ServerByID(servers []string, id string) (string, bool) {
	for _, s := range servers {
		if ServerID(s) == id {
			return s, true
		}
	}
	return "", false
}

// ServerDisplayName is the name a server shows by (GUIDELINES §2): its host
// name, without the user, the rule the app's ServerName.display follows,
// until Add a Server (2.9) lets the person name it.
func ServerDisplayName(target string) string {
	if target == "this-mac" {
		return "This Mac"
	}
	if i := strings.LastIndex(target, "@"); i >= 0 {
		return target[i+1:]
	}
	return target
}
