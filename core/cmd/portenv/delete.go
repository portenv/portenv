// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"google.golang.org/grpc"

	daemonv1 "github.com/portenv/portenv/proto/gen/go/portenv/daemon/v1"
)

// deleting is portenvd's two calls for Delete Workspace (ADR 0029).
type deleting interface {
	GetDeletePlan(ctx context.Context, in *daemonv1.GetDeletePlanRequest, opts ...grpc.CallOption) (*daemonv1.GetDeletePlanResponse, error)
	DeleteBox(ctx context.Context, in *daemonv1.DeleteBoxRequest, opts ...grpc.CallOption) (*daemonv1.DeleteBoxResponse, error)
}

// deleteWorkspace is `portenv app delete BOX [--yes-delete-everything]
// [--saves-elsewhere-stay]`, through portenvd with the app's checks. It
// needs the flag, or the name typed at a terminal. It never touches the
// Keychain: when the key remains, it says to finish in the app.
func deleteWorkspace(ctx context.Context, c deleting, name string, args []string, in io.Reader, out io.Writer, terminal bool) error {
	for _, a := range args {
		if a != "--yes-delete-everything" && a != "--saves-elsewhere-stay" {
			return usageError{Msg: "app delete BOX takes --yes-delete-everything and --saves-elsewhere-stay"}
		}
	}
	yes := slices.Contains(args, "--yes-delete-everything")
	elsewhereOK := slices.Contains(args, "--saves-elsewhere-stay")
	if !yes && !terminal {
		return usageError{Msg: "app delete needs --yes-delete-everything, or a terminal to type the workspace's name at"}
	}
	plan, err := c.GetDeletePlan(ctx, &daemonv1.GetDeletePlanRequest{Name: name})
	if err != nil {
		return plain(err)
	}
	if plan.GetRefusal() != "" {
		return errors.New(plan.GetRefusal())
	}
	read := bufio.NewReader(in)
	if plan.GetSavesElsewhere() && !elsewhereOK {
		if !terminal {
			return usageError{Msg: fmt.Sprintf("The saves of %s are on %s and stay there, with this Mac's key. Add --saves-elsewhere-stay to confirm.", name, plan.GetSavesPlace())}
		}
		_, _ = fmt.Fprintf(out, "The saves of %s are on %s. They stay there, with this Mac's key, so it can be restored later. Continue? (yes/no) ", name, plan.GetSavesPlace())
		if line, _ := read.ReadString('\n'); strings.TrimSpace(line) != "yes" {
			return errors.New("nothing was deleted")
		}
		elsewhereOK = true
	}
	confirm := name
	if !yes {
		if plan.GetSavesElsewhere() {
			_, _ = fmt.Fprintf(out, "This deletes %s from this Mac: its files here. It can't be undone.\n", name)
		} else {
			_, _ = fmt.Fprintf(out, "This deletes %s: its files, its %d saves on this Mac and, from the app, its key. It can't be undone.\n", name, plan.GetSaveCount())
		}
		_, _ = fmt.Fprintf(out, "Type the workspace's name to delete it: ")
		line, _ := read.ReadString('\n')
		confirm = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if confirm != name {
			return errors.New("the name didn't match: nothing was deleted")
		}
	}
	r, err := c.DeleteBox(ctx, &daemonv1.DeleteBoxRequest{Name: name, ConfirmName: confirm, ConfirmSavesElsewhere: elsewhereOK})
	if err != nil {
		return plain(err)
	}
	if len(r.GetDeleteKeyIds()) > 0 {
		// Only the app touches the Keychain.
		return fmt.Errorf("not finished: %s's files and saves are deleted, and its key is still in this Mac's Keychain. Open Portenv and choose Delete Workspace… on %s to finish", name, name)
	}
	if left := r.GetSavesLeftAt(); left != "" {
		_, _ = fmt.Fprintf(out, "Deleted %s from this Mac. Its saves stay on %s, with this Mac's key.\n", name, left)
		return nil
	}
	_, _ = fmt.Fprintf(out, "Deleted %s.\n", name)
	return nil
}
