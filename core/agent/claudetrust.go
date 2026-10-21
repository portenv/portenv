// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// Claude Code's folder trust (R-0411 on CC-0690): Claude Code asks "Is
// this a project you created or one you trust?" the first time it starts
// in a folder, and records the answer in ~/.claude.json as
// projects["<abs path>"].hasTrustDialogAccepted. For an approved team's
// agent, Portenv writes that one key for the one folder the runner
// starts in, as the agent, before the runner starts; nothing is typed
// into the runner. ADR 0025 §9's trust_setting.

// trustClaude is the registry's trust setting for Claude Code.
const trustClaude = "claude"

// maxClaudeJSON caps the ~/.claude.json Portenv reads and rewrites; a
// bigger one is left alone.
const maxClaudeJSON = 16 << 20

// errClaudeJSONUnparsable means ~/.claude.json isn't a JSON object Portenv
// can merge into exactly; it's left untouched.
var errClaudeJSONUnparsable = errors.New("its .claude.json couldn't be read as JSON, so it was left as it is")

// runnerStartFolder is where the runner starts: the agent's clone, or its
// home without one.
func runnerStartFolder(home, workDir string) string {
	if workDir != "" {
		return workDir
	}
	return home
}

// writeClaudeTrust runs team-io claude-trust as the agent. A failure is
// one log line and never stops the start: Claude Code then asks, as it
// did before.
func (s *apiServer) writeClaudeTrust(ctx context.Context, id string, uid int, home, folder string) {
	args := []string{"claude-trust", home, folder}
	var err error
	if s.runHomeIO != nil {
		_, err = s.runHomeIO(ctx, uid, agentGroups(uid, false), args, nil)
	} else {
		_, err = runAsAgent(ctx, uid, append([]string{"team-io"}, args...), nil, func(w io.Writer) error { return TeamIO(args, nil, w) })
	}
	if err != nil {
		slog.Warn("Claude Code's folder trust wasn't written; it will ask once", "agent", id, "err", err)
	}
}

// claudeTrust sets projects[folder].hasTrustDialogAccepted in
// home/.claude.json, as the agent. folder is the home or one folder
// directly in it. The home and the file are opened with O_NOFOLLOW, so a
// planted link is refused, never followed. Every other key is kept as it
// was; a file that isn't a strict JSON object is left untouched. The new
// file is written beside the old one (0600) and renamed over it.
func claudeTrust(home, folder string, _ io.Writer) error {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return fmt.Errorf("%q isn't an agent's home", home)
	}
	if folder != home {
		name, ok := strings.CutPrefix(folder, home+"/")
		if !ok || name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\x00") || !plainText(name, false) {
			return fmt.Errorf("%q isn't the agent's home or a folder in it", folder)
		}
	}
	dfd, err := openDirs(home, nil, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()

	old, err := readAt(dfd, ".claude.json")
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	exists := err == nil
	out, changed, err := mergeClaudeTrust(old, exists, folder)
	if err != nil || !changed {
		return err
	}
	return replaceAtMode(dfd, ".claude.json", out, 0o600)
}

// readAt reads name in the folder dfd, refusing a link and anything that
// isn't a regular file of at most maxClaudeJSON bytes.
func readAt(dfd int, name string) ([]byte, error) {
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
			return nil, fmt.Errorf("%s is a link; it isn't followed", name)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name) // #nosec G115 -- a descriptor
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a file", name)
	}
	if st.Size() > maxClaudeJSON {
		return nil, fmt.Errorf("%s is over %d MB; it was left as it is", name, maxClaudeJSON>>20)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxClaudeJSON+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxClaudeJSON {
		return nil, fmt.Errorf("%s is over %d MB; it was left as it is", name, maxClaudeJSON>>20)
	}
	return b, nil
}

// replaceAtMode writes data to a new file with mode beside name and
// renames it over name.
func replaceAtMode(dfd int, name string, data []byte, mode os.FileMode) error {
	var r [8]byte
	if _, err := rand.Read(r[:]); err != nil {
		return err
	}
	tmp := name + ".portenv-" + hex.EncodeToString(r[:])
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp) // #nosec G115 -- a descriptor
	fail := func(err error) error {
		_ = f.Close()
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dfd, tmp, dfd, name); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	return unix.Fsync(dfd)
}

// mergeClaudeTrust returns old with projects[folder].hasTrustDialogAccepted
// set to true, and whether that changed anything. Only the top level,
// projects and folder's entry are parsed; every other value is carried as
// its own bytes and never read.
func mergeClaudeTrust(old []byte, exists bool, folder string) ([]byte, bool, error) {
	top := map[string]json.RawMessage{}
	if exists {
		if err := strictObject(old, &top); err != nil {
			return nil, false, err
		}
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok {
		if err := strictObject(raw, &projects); err != nil {
			return nil, false, err
		}
	}
	entry := map[string]json.RawMessage{}
	if raw, ok := projects[folder]; ok {
		if err := strictObject(raw, &entry); err != nil {
			return nil, false, err
		}
	}
	if string(entry["hasTrustDialogAccepted"]) == "true" {
		return nil, false, nil
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	var err error
	if projects[folder], err = encodeJSON(entry); err != nil {
		return nil, false, err
	}
	if top["projects"], err = encodeJSON(projects); err != nil {
		return nil, false, err
	}
	out, err := encodeJSON(top)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// strictObject decodes b, exactly one JSON object with no key twice, into
// m.
func strictObject(b []byte, m *map[string]json.RawMessage) error {
	if !json.Valid(b) || noDuplicateKeys(b) != nil {
		return errClaudeJSONUnparsable
	}
	t := bytes.TrimSpace(b)
	if len(t) == 0 || t[0] != '{' {
		return errClaudeJSONUnparsable
	}
	if err := json.Unmarshal(t, m); err != nil {
		return errClaudeJSONUnparsable
	}
	return nil
}

// noDuplicateKeys walks b's tokens and refuses an object with a key twice
// (encoding/json would keep the last silently).
func noDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := k.(string)
				if seen[key] {
					return errClaudeJSONUnparsable
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errClaudeJSONUnparsable
	}
	return nil
}

// encodeJSON marshals v without HTML escaping, so carried values keep
// their characters.
func encodeJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// maxRunnerSettings caps a runner's settings file.
const maxRunnerSettings = 64 << 10

// settingsFlagRE is a runner's settings flag: one long option, nothing
// joined to it.
var settingsFlagRE = regexp.MustCompile(`^--[a-z][a-z-]{0,31}$`)

// checkRunnerSettings checks a start's runner settings before anything is
// made: both or neither, a plain flag, and one JSON object.
func checkRunnerSettings(settings []byte, flag string) error {
	if len(settings) == 0 && flag == "" {
		return nil
	}
	if !settingsFlagRE.MatchString(flag) {
		return fmt.Errorf("%q isn't a settings flag", flag)
	}
	if len(settings) > maxRunnerSettings {
		return fmt.Errorf("the runner's settings are over %d KB", maxRunnerSettings>>10)
	}
	if t := bytes.TrimSpace(settings); len(t) == 0 || t[0] != '{' || !json.Valid(t) {
		return errors.New("the runner's settings aren't a JSON object")
	}
	return nil
}

// runnerSettingsFile is the settings file's name in the agent's run
// folder.
const runnerSettingsFile = "runner-settings.json"

// writeRunnerSettings writes the runner's settings into dir, the agent's
// run folder (the helper's, the agent's group, 0710: the agent can read
// a file there by name but can't change or replace it), 0640, through a
// new file renamed over the old one; a link there is replaced, never
// followed.
func writeRunnerSettings(dir string, settings []byte) error {
	dfd, err := unix.Open(dir, dirFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	return replaceAtMode(dfd, runnerSettingsFile, settings, 0o640)
}
