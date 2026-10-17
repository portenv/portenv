// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The owner approves each agent file's content before it runs (ADR 0022
// §3). An approval pins the file's SHA-256; an edited file is no longer
// approved until the owner approves it again. The approvals live in
// portenvd's folder for the workspace, never in the project or the box,
// so nothing in the workspace can approve itself.

const agentApprovalsFile = "agent-approvals.json"

// agentApprovalsVersion is the store's format. A store of another version
// is unreadable to this portenvd, and left as it is.
const agentApprovalsVersion = 1

type agentApproval struct {
	SHA256 string `json:"sha256"`
	// Content is the approved file, for the app's diff when it changes.
	Content    string    `json:"content"`
	ApprovedAt time.Time `json:"approved_at"`
}

type agentApprovalStore struct {
	Version int                      `json:"version"`
	Agents  map[string]agentApproval `json:"agents"`
}

// agentApprovalsMu keeps two approvals from losing one another.
var agentApprovalsMu sync.Mutex

// loadAgentApprovals reads a workspace's approvals. A missing store is
// nothing approved. A store that can't be read or understood is an error:
// nothing in it is trusted, and it is never written over.
func loadAgentApprovals(stateDir string) (map[string]agentApproval, error) {
	p := filepath.Join(stateDir, agentApprovalsFile)
	b, err := os.ReadFile(p) // #nosec G304 -- portenvd's own folder for the workspace
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]agentApproval{}, nil
	}
	unreadable := func(err error) error {
		return fmt.Errorf("the agent approvals at %s can't be read, so no agent file counts as approved; the file is left as it is: %w", p, err)
	}
	if err != nil {
		return nil, unreadable(err)
	}
	var s agentApprovalStore
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, unreadable(err)
	}
	if dec.More() {
		return nil, unreadable(errors.New("more than one value"))
	}
	if s.Version != agentApprovalsVersion {
		return nil, unreadable(fmt.Errorf("version %d", s.Version))
	}
	for id, a := range s.Agents {
		if !agentIDRE.MatchString(id) || !sha256HexRE.MatchString(a.SHA256) {
			return nil, unreadable(fmt.Errorf("a bad entry for %q", id))
		}
	}
	if s.Agents == nil {
		s.Agents = map[string]agentApproval{}
	}
	return s.Agents, nil
}

// ApproveAgentFile approves agent id's file as it is now, if sum (the
// SHA-256 the app showed the owner) is its content's. A file that changed
// since is refused: the owner approves only what they saw.
func ApproveAgentFile(src TeamSource, id, sum string) error {
	if err := src.check(); err != nil {
		return err
	}
	if !agentIDRE.MatchString(id) {
		return fmt.Errorf("%q isn't an agent id", id)
	}
	agentApprovalsMu.Lock()
	defer agentApprovalsMu.Unlock()
	approvals, err := loadAgentApprovals(src.StateDir)
	if err != nil {
		return err
	}
	dir := src.agentsDir()
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s has no agent file to approve", id)
	}
	name := id + ".md"
	file := filepath.Join(dir, name)
	fi, err := os.Lstat(file)
	if err != nil {
		return fmt.Errorf("%s has no agent file to approve", id)
	}
	p := problems{who: id}
	content, ok := readAgentFile(file, name, fi.Mode().Type(), &p)
	if !ok {
		return errors.New(p.lines[0])
	}
	h := sha256.Sum256(content)
	if sum != hex.EncodeToString(h[:]) {
		return fmt.Errorf("%s's file changed since it was shown; nothing was approved. Look at it again, then approve", id)
	}
	approvals[id] = agentApproval{SHA256: sum, Content: string(content), ApprovedAt: time.Now().UTC()}
	b, err := json.MarshalIndent(agentApprovalStore{Version: agentApprovalsVersion, Agents: approvals}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAtomic(filepath.Join(src.StateDir, agentApprovalsFile), append(b, '\n'))
}

// writePrivateAtomic writes data to path (0600) through a temporary file,
// synced, then renamed, then the folder synced: a crash leaves the old
// file or the new one, never part of one.
func writePrivateAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	d, err := os.Open(dir) // #nosec G304 -- portenvd's own folder for the workspace
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
