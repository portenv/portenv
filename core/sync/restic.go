// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// snapshotHost is the restic host of every snapshot, on every machine, so
// snapshots of one box form one group wherever they were made.
const snapshotHost = "portenv"

// minResticVersion is the oldest restic with restore --overwrite if-changed
// and --delete.
var minResticVersion = [3]int{0, 17, 0}

// restic runs restic against one box's repository through an Executor.
type restic struct {
	exec     Executor
	repo     string
	password []byte
	env      []string
}

// run runs restic with the repository, cache and password set, and returns
// its standard output. The password reaches restic through an inherited pipe
// (RESTIC_PASSWORD_FILE=/dev/fd/3): never on disk, never in the environment.
// A stale lock left by a killed restic is removed once and the command
// retried.
func (r *restic) run(ctx context.Context, args ...string) ([]byte, error) {
	out, stderr, err := r.runOnce(ctx, args...)
	if err != nil && isLockError(stderr) && ctx.Err() == nil {
		if _, _, uerr := r.runOnce(ctx, "unlock"); uerr == nil {
			out, stderr, err = r.runOnce(ctx, args...)
		}
	}
	if err != nil {
		return out, resticError(args, stderr, err)
	}
	return out, nil
}

func (r *restic) runOnce(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	res, err := r.exec.Restic(ctx, append([]string{"--repo", r.repo}, args...), r.password, r.env)
	if err != nil {
		return nil, nil, err
	}
	if res.ExitCode != 0 {
		return res.Stdout, res.Stderr, &ExitError{Code: res.ExitCode}
	}
	return res.Stdout, res.Stderr, nil
}

func isLockError(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "repository is already locked") || strings.Contains(s, "unable to create lock")
}

func resticError(args []string, stderr []byte, err error) error {
	cmd := "restic"
	if len(args) > 0 {
		cmd += " " + args[0]
	}
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	if tail := strings.TrimSpace(strings.Join(lines, "\n")); tail != "" {
		return fmt.Errorf("%s: %w: %s", cmd, err, tail)
	}
	return fmt.Errorf("%s: %w", cmd, err)
}

var versionRE = regexp.MustCompile(`^restic (\d+)\.(\d+)\.(\d+)`)

// checkVersion fails if restic is older than minResticVersion.
func (r *restic) checkVersion(ctx context.Context) error {
	res, err := r.exec.Restic(ctx, []string{"version"}, nil, nil)
	if err != nil {
		return fmt.Errorf("run restic version: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("restic version exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	out := res.Stdout
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return fmt.Errorf("unrecognised restic version output %q", strings.TrimSpace(string(out)))
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	for i := range v {
		if v[i] != minResticVersion[i] {
			if v[i] < minResticVersion[i] {
				return fmt.Errorf("restic %d.%d.%d is too old; need %d.%d.%d or later", v[0], v[1], v[2],
					minResticVersion[0], minResticVersion[1], minResticVersion[2])
			}
			break
		}
	}
	return nil
}

// initIfMissing creates the repository unless it exists.
func (r *restic) initIfMissing(ctx context.Context) error {
	_, stderr, err := r.runOnce(ctx, "cat", "config")
	if err == nil {
		return nil
	}
	var exit *ExitError
	// restic exits 10 when the repository does not exist.
	if !errors.As(err, &exit) || exit.Code != 10 {
		return resticError([]string{"cat"}, stderr, err)
	}
	_, err = r.run(ctx, "init")
	return err
}

// snapshots returns every snapshot, oldest first, with tags parsed.
func (r *restic) snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := r.run(ctx, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("parse restic snapshots: %w", err)
	}
	for i := range snaps {
		snaps[i].parseTags()
	}
	sort.SliceStable(snaps, func(i, j int) bool {
		if !snaps[i].Time.Equal(snaps[j].Time) {
			return snaps[i].Time.Before(snaps[j].Time)
		}
		return snaps[i].ID < snaps[j].ID
	})
	return snaps, nil
}

type backupArgs struct {
	home        string
	tags        []string
	parent      string // snapshot ID; empty means none
	excludeFile string // empty means none
}

// backup saves home and returns the new snapshot's ID.
func (r *restic) backup(ctx context.Context, a backupArgs) (string, error) {
	args := []string{"backup", "--json", "--host", snapshotHost,
		"--ignore-inode", "--ignore-ctime", "--exclude-caches"}
	if a.excludeFile != "" {
		args = append(args, "--exclude-file", a.excludeFile)
	}
	if a.parent != "" {
		args = append(args, "--parent", a.parent)
	}
	for _, t := range a.tags {
		args = append(args, "--tag", t)
	}
	args = append(args, a.home)
	out, err := r.run(ctx, args...)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var msg struct {
			Type       string `json:"message_type"`
			SnapshotID string `json:"snapshot_id"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Type == "summary" && msg.SnapshotID != "" {
			return msg.SnapshotID, nil
		}
	}
	return "", errors.New("restic backup finished without reporting a snapshot ID")
}

// restoreTo restores snapshot s into target, downloading only changed
// files. With deleteExtra, files in target that are not in s are removed.
func (r *restic) restoreTo(ctx context.Context, s Snapshot, target string, deleteExtra bool) error {
	if len(s.Paths) != 1 {
		return fmt.Errorf("snapshot %s has paths %v, want exactly one", s.short(), s.Paths)
	}
	args := []string{"restore", s.ID + ":" + s.Paths[0], "--target", target, "--overwrite", "if-changed"}
	if deleteExtra {
		args = append(args, "--delete")
	}
	_, err := r.run(ctx, args...)
	return err
}

// tag adds and removes tags on one snapshot. restic rewrites the snapshot,
// so its ID changes: callers must re-read snapshots afterwards.
func (r *restic) tag(ctx context.Context, id string, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	args := []string{"tag"}
	for _, t := range add {
		args = append(args, "--add", t)
	}
	for _, t := range remove {
		args = append(args, "--remove", t)
	}
	_, err := r.run(ctx, append(args, id)...)
	return err
}

// untag removes one tag from several snapshots in one call. Their IDs
// change.
func (r *restic) untag(ctx context.Context, ids []string, tag string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.run(ctx, append([]string{"tag", "--remove", tag}, ids...)...)
	return err
}

// forget applies the retention policy from docs/PLAN.md.
func (r *restic) forget(ctx context.Context, prune bool) error {
	args := []string{"forget", "--group-by", "host",
		"--keep-last", "20", "--keep-hourly", "24", "--keep-daily", "14",
		"--keep-weekly", "8", "--keep-monthly", "12", "--keep-tag", tagOrphaned}
	if prune {
		args = append(args, "--prune")
	}
	_, err := r.run(ctx, args...)
	return err
}
