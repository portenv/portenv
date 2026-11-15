// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/portenv/portenv/core/internal/tzname"
	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
)

// The image's tz database and the folder holding the box's clock.
const (
	zoneinfoDir = "/usr/share/zoneinfo"
	etcDir      = "/etc"
)

// workspaceZone is the zone this box's clock follows (R-0411), set at
// each start by portenvd's SetTimeZone; "" until then. One box per
// process, so it's the process's.
var workspaceZone struct {
	sync.RWMutex
	name string
}

func setWorkspaceZone(name string) {
	workspaceZone.Lock()
	defer workspaceZone.Unlock()
	workspaceZone.name = name
}

// zoneEnv is TZ for a shell or runner started now, or nothing before the
// first SetTimeZone (the image's own clock).
func zoneEnv() []string {
	workspaceZone.RLock()
	defer workspaceZone.RUnlock()
	if workspaceZone.name == "" {
		return nil
	}
	return []string{"TZ=" + workspaceZone.name}
}

// envFor is the environment of a terminal as the main user.
func envFor(cfg Config) []string {
	return append([]string{
		"HOME=" + cfg.Home(), "USER=" + cfg.User, "LOGNAME=" + cfg.User,
		"SHELL=/bin/bash", "TERM=xterm-256color", "LANG=C.UTF-8", "COLORTERM=truecolor",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}, zoneEnv()...)
}

// SetTimeZone on the agent channel checks the name's shape, then relays to
// init, which alone writes /etc/localtime; the zone init applied is what
// later shells and agent runners get as TZ.
func (s *apiServer) SetTimeZone(ctx context.Context, req *agentv1.SetTimeZoneRequest) (*agentv1.SetTimeZoneResponse, error) {
	if s.setLocaltime == nil {
		return nil, status.Error(codes.Unimplemented, "this workspace can't set its time zone")
	}
	zone := req.GetZone()
	if !tzname.Valid(zone) {
		zone = tzname.UTC
	}
	applied, err := s.setLocaltime(ctx, zone)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "set the time zone: %v", err)
	}
	if !tzname.Valid(applied) {
		return nil, status.Errorf(codes.Internal, "init applied %q, not a zone name", applied)
	}
	setWorkspaceZone(applied)
	return &agentv1.SetTimeZoneResponse{Zone: applied}, nil
}

// SetTimeZone is init's, as root: it links /etc/localtime to the zone,
// or to UTC when the image's tz database doesn't have it.
func (s *server) SetTimeZone(_ context.Context, req *agentv1.SetTimeZoneRequest) (*agentv1.SetTimeZoneResponse, error) {
	etc, zi := s.etcDir, s.zoneinfoDir
	if etc == "" {
		etc = etcDir
	}
	if zi == "" {
		zi = zoneinfoDir
	}
	applied, err := linkLocaltime(etc, zi, req.GetZone())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &agentv1.SetTimeZoneResponse{Zone: applied}, nil
}

// askInitTimeZone asks init on its local socket.
func askInitTimeZone(socket string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, zone string) (string, error) {
		conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return "", fmt.Errorf("connect to init: %w", err)
		}
		defer func() { _ = conn.Close() }()
		r, err := agentv1.NewAgentServiceClient(conn).SetTimeZone(ctx, &agentv1.SetTimeZoneRequest{Zone: zone})
		if err != nil {
			return "", errors.New(status.Convert(err).Message())
		}
		return r.GetZone(), nil
	}
}

// zoneFile says whether zone is a regular file in the tz database at
// zoneinfo, resolving any link only inside it.
func zoneFile(zoneinfo, zone string) bool {
	if !tzname.Valid(zone) {
		return false
	}
	root, err := filepath.EvalSymlinks(zoneinfo)
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(filepath.Join(root, zone))
	if err != nil || !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// linkLocaltime points etc/localtime at zoneinfo/<zone>, or at
// zoneinfo/UTC when zone isn't in the database, and returns the zone
// applied. The new link is made beside the old one and renamed over it,
// so the clock is never missing. Without a tz database the clock stays
// UTC and nothing is changed.
func linkLocaltime(etc, zoneinfo, zone string) (string, error) {
	if !zoneFile(zoneinfo, zone) {
		zone = tzname.UTC
		if !zoneFile(zoneinfo, zone) {
			return zone, nil
		}
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tmp := filepath.Join(etc, ".localtime-"+hex.EncodeToString(b[:]))
	if err := os.Symlink(filepath.Join(zoneinfo, zone), tmp); err != nil {
		return "", fmt.Errorf("link the time zone: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(etc, "localtime")); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("link the time zone: %w", err)
	}
	return zone, nil
}
