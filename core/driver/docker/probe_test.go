// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/portenv/portenv/core/driver"
	"github.com/portenv/portenv/core/driver/drivertest"
)

// probe runs one command in the box for a test to inspect it: test code
// only, so the driver itself has no way to exec into a box (ADR 0010
// condition 4, ADR 0014).
func (d *Driver) probe(ctx context.Context, id driver.BoxID, req drivertest.ProbeRequest) (drivertest.ProbeResult, error) {
	if len(req.Argv) == 0 {
		return drivertest.ProbeResult{}, errors.New("exec: empty argv")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	return d.execNamed(ctx, d.containerName(id), req)
}

// execIn runs argv as root in a container by name (tests).
func (d *Driver) execIn(ctx context.Context, name string, argv []string) (drivertest.ProbeResult, error) {
	return d.execNamed(ctx, name, drivertest.ProbeRequest{Argv: argv})
}

func (d *Driver) execNamed(ctx context.Context, name string, req drivertest.ProbeRequest) (drivertest.ProbeResult, error) {
	user := req.User
	if user == "" {
		user = "root"
	}
	created, err := d.cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		User: user, Cmd: req.Argv, Env: req.Env,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return drivertest.ProbeResult{}, fmt.Errorf("exec create: %w", err)
	}
	att, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return drivertest.ProbeResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()

	writeErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(att.Conn, bytes.NewReader(req.Stdin))
		if cerr := att.CloseWrite(); err == nil {
			err = cerr
		}
		writeErr <- err
	}()
	var stdout, stderr bytes.Buffer
	readDone := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			return drivertest.ProbeResult{}, fmt.Errorf("exec output: %w", err)
		}
	case <-ctx.Done():
		return drivertest.ProbeResult{}, ctx.Err()
	}
	if err := <-writeErr; err != nil {
		return drivertest.ProbeResult{}, fmt.Errorf("exec stdin: %w", err)
	}
	insp, err := d.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return drivertest.ProbeResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	return drivertest.ProbeResult{ExitCode: insp.ExitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}
