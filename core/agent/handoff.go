// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/portenv/portenv/proto/gen/go/portenv/agent/v1"
	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Hand-offs (R-0257 item 3): files from the sender's home or the project,
// read as the sender, written into the recipient's ~/inbox/<sender>/<time>/
// as the recipient. Regular files only, never through a link, at most
// 100 MB, within the recipient's quota, or nothing lands.

const (
	maxHandoffBytes = 100 << 20
	maxHandoffFiles = 10000
)

type handoffHeader struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// handoffPack writes the files under base/rel as frames: a JSON header
// line, then the bytes. Any link or special file refuses the whole
// hand-off.
func handoffPack(base, rel string, w io.Writer) error {
	parts, err := homeRel(rel, false)
	if err != nil {
		return err
	}
	dfd, err := openDirs(base, parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	var total int64
	files := 0
	var walk func(dir int, name, out string) error
	walk = func(dir int, name, out string) error {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			files++
			total += st.Size
			if files > maxHandoffFiles || total > maxHandoffBytes {
				return fmt.Errorf("a hand-off is at most %d MB and %d files", maxHandoffBytes>>20, maxHandoffFiles)
			}
			fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			f := os.NewFile(uintptr(fd), out) // #nosec G115 -- a descriptor
			defer func() { _ = f.Close() }()
			h, _ := json.Marshal(handoffHeader{Path: out, Size: st.Size})
			if _, err := w.Write(append(h, '\n')); err != nil {
				return err
			}
			n, err := io.CopyN(w, f, st.Size)
			if err != nil || n != st.Size {
				return fmt.Errorf("%s changed while it was read", out)
			}
			return nil
		case unix.S_IFDIR:
			sub, err := unix.Openat(dir, name, dirFlags, 0)
			if err != nil {
				return err
			}
			d := os.NewFile(uintptr(sub), out) // #nosec G115 -- a descriptor
			defer func() { _ = d.Close() }()
			names, err := d.Readdirnames(-1)
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := walk(sub, n, path.Join(out, n)); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf("%s is a link or a special file; only regular files are handed over", out)
	}
	return walk(dfd, parts[len(parts)-1], parts[len(parts)-1])
}

// handoffUnpack reads frames and writes them under home/dest, as new files,
// after checking the whole hand-off fits the quota. It prints the files.
func handoffUnpack(home, dest, quota string, r io.Reader, w io.Writer) error {
	destParts, err := homeRel(dest, false)
	if err != nil {
		return err
	}
	limit, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || limit <= 0 {
		return errors.New("no quota given")
	}
	type file struct {
		rel  []string
		data []byte
	}
	var files []file
	var total int64
	br := bufio.NewReader(io.LimitReader(r, maxHandoffBytes+int64(maxHandoffFiles)*4096))
	for {
		line, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil {
			return err
		}
		var h handoffHeader
		if err := json.Unmarshal(line, &h); err != nil {
			return err
		}
		rel, err := homeRel(h.Path, false)
		if err != nil || h.Size < 0 {
			return fmt.Errorf("a bad path in the hand-off: %q", h.Path)
		}
		total += h.Size
		if total > maxHandoffBytes || len(files) >= maxHandoffFiles {
			return fmt.Errorf("a hand-off is at most %d MB", maxHandoffBytes>>20)
		}
		data := make([]byte, h.Size)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		files = append(files, file{rel: rel, data: data})
	}
	used, err := homeSize(home)
	if err != nil {
		return err
	}
	if used+total > limit {
		return errHomeFull
	}
	var out []*typesv1.HandoffFile
	for _, f := range files {
		all := append(append([]string{}, destParts...), f.rel...)
		dfd, err := openDirs(home, all[:len(all)-1], true)
		if err != nil {
			return err
		}
		fd, err := unix.Openat(dfd, all[len(all)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		_ = unix.Close(dfd)
		if err != nil {
			return err
		}
		fh := os.NewFile(uintptr(fd), f.rel[len(f.rel)-1]) // #nosec G115 -- a descriptor
		_, werr := fh.Write(f.data)
		serr := fh.Sync()
		cerr := fh.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			return err
		}
		h := sha256.Sum256(f.data)
		out = append(out, &typesv1.HandoffFile{Path: strings.Join(all, "/"), Sha256: hex.EncodeToString(h[:]), Size: int64(len(f.data))})
	}
	return json.NewEncoder(w).Encode(out)
}

// handoffSource maps a box path to the base it may come from (the
// sender's home or a project folder) and the path inside it.
func (s *apiServer) handoffSource(from, src string) (base, rel string, err error) {
	if !strings.HasPrefix(src, "/") || path.Clean(src) != src {
		return "", "", fmt.Errorf("%q isn't a full, plain path", src)
	}
	if r, ok := strings.CutPrefix(src, "/home/"+from+"/"); ok {
		return filepath.Join(s.cfg.HomeRoot, from), r, nil
	}
	if r, ok := strings.CutPrefix(src, "/home/"+s.cfg.User+"/"); ok {
		project, inside, found := strings.Cut(r, "/")
		if found && project != "" && !strings.HasPrefix(project, ".") && inside != "" {
			return filepath.Join(s.cfg.HomeRoot, s.cfg.User, project), inside, nil
		}
	}
	return "", "", fmt.Errorf("hand over files from your home or the project, not %s", src)
}

// HandoffFiles copies a hand-off into the recipient's inbox (R-0257 item 3).
func (s *apiServer) HandoffFiles(ctx context.Context, req *agentv1.HandoffFilesRequest) (*agentv1.HandoffFilesResponse, error) {
	from, to := req.GetFromAgent(), req.GetToAgent()
	if from == to {
		return nil, status.Error(codes.InvalidArgument, "an agent can't hand files to itself")
	}
	fromUID, _, err := s.agentHome(from)
	if err != nil {
		return nil, err
	}
	toUID, toHome, err := s.agentHome(to)
	if err != nil {
		return nil, err
	}
	base, rel, err := s.handoffSource(from, req.GetSourcePath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.GetRecipientQuotaBytes() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "no quota given")
	}
	// Read as the sender: from its own home with its own group only, from
	// the project with group team too, as the team reads it (CC-0507,
	// R-0334). Written as the recipient, with its own group only.
	pack := s.homeIO
	if base != filepath.Join(s.cfg.HomeRoot, from) {
		pack = s.projectIO
	}
	packed, err := pack(ctx, fromUID, []string{"handoff-pack", base, rel}, nil)
	if err != nil {
		return nil, homeErr(err)
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	dest := "inbox/" + from + "/" + now().UTC().Format("20060102T150405Z")
	out, err := s.homeIO(ctx, toUID, []string{"handoff-unpack", toHome, dest, strconv.FormatInt(req.GetRecipientQuotaBytes(), 10)}, bytes.NewReader(packed))
	if err != nil {
		return nil, homeErr(err)
	}
	var files []*typesv1.HandoffFile
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &agentv1.HandoffFilesResponse{Files: files}, nil
}
