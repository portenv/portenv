// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	typesv1 "github.com/portenv/portenv/proto/gen/go/portenv/types/v1"
)

// Agent files (ADR 0022 §3). A team is described in the repository, one
// file per agent at <project>/.portenv/agents/<id>.md, so it travels with
// the code. portenvd reads and checks each one before anything starts,
// pins it by its SHA-256, and starts only content the owner approved.
// A personal override on the Mac may change a few things and narrow the
// grant, never widen it.

const (
	maxAgentFileBytes         = 64 << 10
	maxAgentInstructionsBytes = 32 << 10
	maxAgentNameChars         = 40
	maxAgentRoleChars         = 120
	// The ceilings of ADR 0022 §7; the owner may set lower limits.
	maxTeamAgents      = 8
	maxAgentPanes      = 8
	defaultTeamAgents  = 4
	defaultAgentPanes  = 4
	agentFilesRelative = ".portenv/agents"
)

var (
	agentIDRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	agentAvatarRE = regexp.MustCompile(`^[a-z0-9.]{1,40}$`)
	agentToolRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	sha256HexRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// reservedAgentIDs can't be agents (ADR 0022 §1): the toolbox image's
// users and groups (Ubuntu 24.04's base, plus Portenv's own), team, and
// owner, which the board uses for the owner. Anything starting with
// portenv is reserved too. init checks the box's real /etc/passwd again
// when it makes the user; this list only gives the line early.
var reservedAgentIDs = map[string]bool{
	"team": true, "work": true, "owner": true,
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true, "games": true,
	"man": true, "lp": true, "mail": true, "news": true, "uucp": true, "proxy": true,
	"www-data": true, "backup": true, "list": true, "irc": true, "nobody": true,
	"ubuntu": true, "adm": true, "tty": true, "disk": true, "kmem": true, "dialout": true,
	"fax": true, "voice": true, "cdrom": true, "floppy": true, "tape": true, "sudo": true,
	"audio": true, "dip": true, "operator": true, "src": true, "shadow": true, "utmp": true,
	"video": true, "sasl": true, "plugdev": true, "staff": true, "users": true, "nogroup": true,
}

func reservedAgentID(id string) bool {
	return reservedAgentIDs[id] || strings.HasPrefix(id, "portenv")
}

// TeamLimits are the owner's limits (ADR 0022 §7). Zero means the default;
// anything over the ceiling is the ceiling.
type TeamLimits struct {
	MaxAgents int
	MaxPanes  int
}

// DefaultTeamLimits is §7's defaults.
func DefaultTeamLimits() TeamLimits {
	return TeamLimits{MaxAgents: defaultTeamAgents, MaxPanes: defaultAgentPanes}
}

func (l TeamLimits) normal() TeamLimits {
	clamp := func(v, def, ceiling int) int {
		if v <= 0 {
			return def
		}
		return min(v, ceiling)
	}
	return TeamLimits{
		MaxAgents: clamp(l.MaxAgents, defaultTeamAgents, maxTeamAgents),
		MaxPanes:  clamp(l.MaxPanes, defaultAgentPanes, maxAgentPanes),
	}
}

// RunnerCheck is the helper's check that a local runner's first word is
// on the agent user's PATH in the box (ADR 0022 §3). portenvd never runs
// the runner to find out. Its error is the rest of a plain line, after
// "<id>: ".
type RunnerCheck func(agentID, program string) error

// TeamSource is where one workspace's team comes from.
type TeamSource struct {
	// ProjectDir is the workspace's project folder. Empty: no team.
	ProjectDir string
	// OverridesDir holds personal overrides by workspace:
	// ~/Library/Application Support/Portenv/agents.local on the Mac.
	OverridesDir string
	// WorkspaceID names the overrides' folder. It must be one folder name.
	WorkspaceID string
	// StateDir is portenvd's folder for this workspace
	// (<Portenv directory>/state/<box id>), which holds the approvals.
	StateDir string
	Limits   TeamLimits
	// CheckRunner, when set, checks each valid local runner.
	CheckRunner RunnerCheck
}

func (src TeamSource) agentsDir() string {
	return filepath.Join(src.ProjectDir, filepath.FromSlash(agentFilesRelative))
}

func (src TeamSource) check() error {
	w := src.WorkspaceID
	if w == "" || w == "." || w == ".." || strings.ContainsAny(w, `/\`) || hasUnsafeRune(w) {
		return fmt.Errorf("workspace id %q can't name a folder", w)
	}
	if src.StateDir == "" {
		return errors.New("no state folder for the workspace's agent approvals")
	}
	return nil
}

// AgentSpec is one agent file as written, after its checks.
type AgentSpec struct {
	ID, Name, Role string
	// RunnerLine is the runner as written, which Approve shows first.
	RunnerLine string
	// Runner is RunnerLine split into an argv; nil for a remote agent.
	Runner     []string
	Remote     bool
	Via        string
	Avatar     string
	ShareLogin []string
	// HomeSeed is cleaned, relative to the project.
	HomeSeed []string
	// Grant is the file's grant, with the defaults filled in.
	Grant        *typesv1.AgentGrant
	Instructions string

	set   map[string]bool // the keys the file sets
	grant grantSet        // grant's keys the file sets
}

// grantSet records which grant keys a file sets, for overrides.
type grantSet struct {
	panes, maxPanes, message, askOwner, packages bool
}

// TeamAgent is one agent of a team: its repository file, the personal
// override applied to it, and whether it may start.
type TeamAgent struct {
	// ID is the file's base name.
	ID string
	// Spec is the repository file; nil when it couldn't be read.
	Spec *AgentSpec
	// Override is whether a personal override is in effect.
	Override bool

	// What's in effect, after the override.
	RunnerLine string
	Runner     []string
	Remote     bool
	Via        string
	Avatar     string
	ShareLogin []string
	Grant      *typesv1.AgentGrant

	// SHA256 pins the repository file's bytes (lowercase hex); empty when
	// it couldn't be read.
	SHA256   string
	Approved bool
	// ApprovedContent is what the owner last approved, for the diff.
	ApprovedContent string
	// Problems are plain lines for the agent's row. Any one stops a start.
	Problems []string
}

// Valid is whether the agent's files have no problems.
func (a TeamAgent) Valid() bool { return a.Spec != nil && len(a.Problems) == 0 }

// Team is a workspace's agents, in id order, and problems with the team
// as a whole.
type Team struct {
	Agents   []TeamAgent
	Problems []string
}

// problems collects plain lines for one file.
type problems struct {
	who   string
	lines []string
}

func (p *problems) addf(format string, a ...any) {
	p.lines = append(p.lines, p.who+": "+fmt.Sprintf(format, a...))
}

// LoadTeam reads, checks and pins a workspace's agent files. A missing
// agents folder is no team. A problem with a file is a line on that agent,
// never an error; the error is for what portenvd can't trust at all, such
// as an approvals store it can't read.
func LoadTeam(src TeamSource) (Team, error) {
	var team Team
	if err := src.check(); err != nil {
		return team, err
	}
	limits := src.Limits.normal()
	approvals, err := loadAgentApprovals(src.StateDir)
	if err != nil {
		return team, err
	}
	if src.ProjectDir == "" {
		return team, nil
	}
	dir := src.agentsDir()
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return team, nil
	case err != nil:
		return team, err
	case !fi.IsDir():
		team.Problems = append(team.Problems, "The project's "+agentFilesRelative+" isn't a folder.")
		return team, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return team, err
	}
	type read struct {
		content []byte
		ok      bool
	}
	var files []read
	members := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		a := TeamAgent{ID: id}
		p := problems{who: id}
		content, ok := readAgentFile(filepath.Join(dir, name), name, e.Type(), &p)
		if ok {
			sum := sha256.Sum256(content)
			a.SHA256 = hex.EncodeToString(sum[:])
		}
		a.Problems = p.lines
		team.Agents = append(team.Agents, a)
		files = append(files, read{content, ok})
		if agentIDRE.MatchString(id) && !reservedAgentID(id) {
			members[id] = true
		}
	}

	for i := range team.Agents {
		a := &team.Agents[i]
		p := problems{who: a.ID, lines: a.Problems}
		if files[i].ok {
			a.Spec = buildAgentSpec(files[i].content, a.ID, false, limits, &p)
		}
		if a.Spec != nil {
			for _, m := range a.Spec.Grant.GetMessage() {
				if m == a.ID || !members[m] {
					p.addf("grant.message names %q, which isn't another agent in this team.", m)
				}
			}
			a.RunnerLine, a.Runner, a.Remote, a.Via = a.Spec.RunnerLine, a.Spec.Runner, a.Spec.Remote, a.Spec.Via
			a.Avatar, a.ShareLogin = a.Spec.Avatar, a.Spec.ShareLogin
			a.Grant = cloneGrant(a.Spec.Grant)
			applyOverride(a, src, limits, &p)
		}
		if rec, ok := approvals[a.ID]; ok {
			a.ApprovedContent = rec.Content
			a.Approved = a.SHA256 != "" && rec.SHA256 == a.SHA256
		}
		if len(p.lines) == 0 && a.Spec != nil && !a.Remote && src.CheckRunner != nil {
			if err := src.CheckRunner(a.ID, a.Runner[0]); err != nil {
				p.lines = append(p.lines, a.ID+": "+err.Error())
			}
		}
		a.Problems = p.lines
	}

	if n := len(team.Agents); n > limits.MaxAgents {
		team.Problems = append(team.Problems, fmt.Sprintf("The team has %d agents; the owner's limit is %d.", n, limits.MaxAgents))
	}
	odir := filepath.Join(src.OverridesDir, src.WorkspaceID)
	if src.OverridesDir != "" {
		oents, err := os.ReadDir(odir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return team, err
		}
		for _, e := range oents {
			name := e.Name()
			if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
				continue
			}
			id := strings.TrimSuffix(name, ".md")
			if !slices.ContainsFunc(team.Agents, func(a TeamAgent) bool { return a.ID == id }) {
				team.Problems = append(team.Problems, fmt.Sprintf("%s's override: there's no agent file %s in the project's %s.", id, name, agentFilesRelative))
			}
		}
	}
	return team, nil
}

// readAgentFile reads one agent file or override: a plain file (never a
// link), at most 64 KB. Its problems go to p; ok is false when there is
// nothing to check.
func readAgentFile(file, name string, typ fs.FileMode, p *problems) ([]byte, bool) {
	if typ&fs.ModeType != 0 {
		p.addf("%s isn't a plain file.", name)
		return nil, false
	}
	// O_NOFOLLOW and the Stat below hold even if the file is swapped after
	// the listing; O_NONBLOCK keeps a swapped-in pipe from hanging portenvd.
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- a file in the project's agents folder, checked as a plain file
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			p.addf("%s isn't a plain file.", name)
		} else {
			p.addf("%s can't be read: %v.", name, pathErrText(err))
		}
		return nil, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		p.addf("%s can't be read: %v.", name, pathErrText(err))
		return nil, false
	}
	if !fi.Mode().IsRegular() {
		p.addf("%s isn't a plain file.", name)
		return nil, false
	}
	if fi.Size() > maxAgentFileBytes {
		p.addf("the file is %d bytes; it must be at most 64 KB.", fi.Size())
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxAgentFileBytes+1))
	if err != nil {
		p.addf("%s can't be read: %v.", name, pathErrText(err))
		return nil, false
	}
	if len(b) > maxAgentFileBytes {
		p.addf("the file is more than %d bytes; it must be at most 64 KB.", maxAgentFileBytes)
		return nil, false
	}
	return b, true
}

func pathErrText(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

var (
	agentFileKeys = []string{"id", "name", "role", "runner", "via", "avatar", "share_login", "home_seed", "grant"}
	agentListLike = map[string]string{"share_login": "[claude]", "home_seed": "[docs/style-guide.md]", "grant.message": "[lead]"}
)

// buildAgentSpec checks one file's content. For an override, missing keys
// are fine and only some keys may be set. It returns nil when there is no
// frontmatter to read.
func buildAgentSpec(content []byte, fileID string, override bool, limits TeamLimits, p *problems) *AgentSpec {
	if !utf8.Valid(content) {
		p.addf("the file isn't valid UTF-8.")
		return nil
	}
	fm, fmProblems, ok := parseFrontmatter(string(content))
	for _, l := range fmProblems {
		p.addf("%s", l)
	}
	if !ok {
		return nil
	}
	s := &AgentSpec{set: map[string]bool{}}
	if override {
		if strings.TrimSpace(fm.body) != "" {
			p.addf("it has instructions; only the repository file's are used.")
		}
	} else {
		if n := len(fm.body); n > maxAgentInstructionsBytes {
			p.addf("the instructions are %d bytes; they must be at most 32 KB.", n)
		}
		s.Instructions = fm.body
	}

	scalar := func(key string, v *fmValue) (string, bool) {
		if v.kind != fmScalar {
			if v.kind == fmList {
				p.addf("%s must be a single value, not a list.", key)
			} else {
				p.addf("%s must be a single value.", key)
			}
			return "", false
		}
		return v.s, true
	}
	list := func(key string, v *fmValue) ([]string, bool) {
		if v.kind != fmList {
			p.addf("%s must be a list, like %s.", key, agentListLike[key])
			return nil, false
		}
		return v.list, true
	}
	safe := func(key, val string) bool {
		if hasUnsafeRune(val) {
			p.addf("%s has a control or bidi character.", key)
			return false
		}
		return true
	}

	maxPanes := int32(min(defaultAgentPanes, limits.MaxPanes)) // #nosec G115 -- at most 8
	g := &typesv1.AgentGrant{
		Panes:    typesv1.GrantMode_GRANT_MODE_ASK,
		MaxPanes: maxPanes,
		AskOwner: true,
		Packages: typesv1.GrantMode_GRANT_MODE_ASK,
	}
	s.Grant = g
	viaSet := false

	for _, key := range fm.keys.order {
		v := fm.keys.vals[key]
		if !slices.Contains(agentFileKeys, key) {
			p.addf("%q isn't a key an agent file may have.", key)
			continue
		}
		if override {
			switch key {
			case "home_seed":
				p.addf("it sets home_seed; an override can't add home_seed.")
				continue
			case "name", "role":
				p.addf("it sets %s; an override may change only runner, via, avatar, share_login and grant.", key)
				continue
			}
		}
		if v.kind == fmBad {
			continue
		}
		if v.kind == fmEmpty {
			p.addf("%s has no value.", key)
			continue
		}
		s.set[key] = true
		switch key {
		case "id":
			id, ok := scalar(key, v)
			switch {
			case !ok:
			case !agentIDRE.MatchString(id):
				p.addf("id %q must be a lowercase letter, then up to 31 lowercase letters, digits or hyphens.", id)
			case id != fileID:
				p.addf("id is %q; it must match the file's name, %s.", id, fileID)
			case reservedAgentID(id):
				p.addf("%q is a reserved name; choose another id.", id)
			default:
				s.ID = id
			}
		case "name":
			if n, ok := scalar(key, v); ok && safe(key, n) {
				if c := utf8.RuneCountInString(n); c < 1 || c > maxAgentNameChars {
					p.addf("name is %d characters; it must be 1 to %d.", c, maxAgentNameChars)
				} else {
					s.Name = n
				}
			}
		case "role":
			if r, ok := scalar(key, v); ok && safe(key, r) {
				if c := utf8.RuneCountInString(r); c > maxAgentRoleChars {
					p.addf("role is %d characters; it must be at most %d.", c, maxAgentRoleChars)
				} else {
					s.Role = r
				}
			}
		case "runner":
			r, ok := scalar(key, v)
			if !ok || !safe(key, r) {
				break
			}
			s.RunnerLine = r
			if r == "remote" {
				s.Remote = true
				break
			}
			argv, err := splitArgv(r)
			switch {
			case errors.Is(err, errArgvEmpty):
				p.addf("runner is empty.")
			case errors.Is(err, errArgvQuote):
				p.addf("runner has an unbalanced quote.")
			case errors.Is(err, errArgvBackslash):
				p.addf("runner ends with a lone backslash.")
			default:
				s.Runner = argv
			}
		case "via":
			viaSet = true
			if via, ok := scalar(key, v); ok {
				if via != "ssh" && via != "mcp" && via != "web" {
					p.addf("via is %q; it must be ssh, mcp or web.", via)
				} else {
					s.Via = via
				}
			}
		case "avatar":
			if a, ok := scalar(key, v); ok {
				if !agentAvatarRE.MatchString(a) {
					p.addf("avatar %q must be an SF Symbol or colour name: lowercase letters, digits and dots, at most 40.", a)
				} else {
					s.Avatar = a
				}
			}
		case "share_login":
			items, _ := list(key, v)
			s.ShareLogin = []string{}
			for _, t := range items {
				if !agentToolRE.MatchString(t) {
					p.addf("share_login %q must be a tool name: a lowercase letter, then lowercase letters, digits or hyphens.", t)
					continue
				}
				s.ShareLogin = append(s.ShareLogin, t)
			}
		case "home_seed":
			items, _ := list(key, v)
			for _, h := range items {
				switch {
				case h == "" || strings.HasPrefix(h, "/") || filepath.IsAbs(h):
					p.addf("home_seed %q must be a path relative to the project.", h)
				case slices.Contains(strings.Split(h, "/"), ".."):
					p.addf("home_seed %q must stay inside the project (no ..).", h)
				case hasUnsafeRune(h):
					p.addf("home_seed has a control or bidi character.")
				default:
					s.HomeSeed = append(s.HomeSeed, path.Clean(h))
				}
			}
		case "grant":
			if v.kind != fmMap {
				p.addf("grant must be settings on the indented lines below it.")
				break
			}
			buildGrant(v.sub, g, &s.grant, override, limits, p)
		}
	}

	if !override {
		for _, key := range []string{"id", "name", "runner"} {
			if _, ok := fm.keys.vals[key]; !ok {
				p.addf("%s is missing.", key)
			}
		}
		checkVia(s.set["runner"], s.Remote, viaSet, p)
	}
	return s
}

// checkVia: a remote runner needs via; a local one takes none.
func checkVia(runnerSet, remote, viaSet bool, p *problems) {
	switch {
	case remote && !viaSet:
		p.addf("runner is remote, so via (ssh, mcp or web) is required.")
	case runnerSet && !remote && viaSet:
		p.addf("via is set, but only runner: remote takes via.")
	}
}

func buildGrant(keys *fmKeys, g *typesv1.AgentGrant, set *grantSet, override bool, limits TeamLimits, p *problems) {
	for _, key := range keys.order {
		v := keys.vals[key]
		full := "grant." + key
		switch key {
		case "panes", "max_panes", "message", "ask_owner", "packages":
		default:
			p.addf("%q isn't a key an agent file may have.", full)
			continue
		}
		if v.kind == fmBad {
			continue
		}
		if v.kind == fmEmpty {
			p.addf("%s has no value.", full)
			continue
		}
		if key == "message" {
			if v.kind != fmList {
				p.addf("%s must be a list, like %s.", full, agentListLike[full])
				continue
			}
			g.Message = slices.Clone(v.list)
			set.message = true
			continue
		}
		if v.kind != fmScalar {
			p.addf("%s must be a single value, not a list.", full)
			continue
		}
		switch key {
		case "panes":
			m, ok := grantModes[v.s]
			if !ok {
				p.addf("grant.panes is %q; it must be ask, allow or never.", v.s)
				continue
			}
			g.Panes, set.panes = m, true
		case "packages":
			m, ok := grantModes[v.s]
			if !ok || m == typesv1.GrantMode_GRANT_MODE_ALLOW {
				p.addf("grant.packages is %q; it must be ask or never.", v.s)
				continue
			}
			g.Packages, set.packages = m, true
		case "ask_owner":
			if v.quoted || (v.s != "true" && v.s != "false") {
				p.addf("grant.ask_owner is %q; it must be true or false.", v.s)
				continue
			}
			g.AskOwner, set.askOwner = v.s == "true", true
		case "max_panes":
			if v.quoted || v.s == "" || strings.Trim(v.s, "0123456789") != "" {
				p.addf("grant.max_panes is %q; it must be a whole number from 1 to %d.", v.s, maxAgentPanes)
				continue
			}
			n, err := strconv.ParseInt(v.s, 10, 32)
			if err != nil || n < 1 || n > maxAgentPanes {
				p.addf("grant.max_panes is %s; it must be 1 to %d.", v.s, maxAgentPanes)
				continue
			}
			if !override && n > int64(limits.MaxPanes) {
				p.addf("grant.max_panes is %d; the owner's limit is %d.", n, limits.MaxPanes)
				continue
			}
			g.MaxPanes, set.maxPanes = int32(n), true // #nosec G115 -- 1 to 8, checked above
		}
	}
}

var grantModes = map[string]typesv1.GrantMode{
	"ask":   typesv1.GrantMode_GRANT_MODE_ASK,
	"allow": typesv1.GrantMode_GRANT_MODE_ALLOW,
	"never": typesv1.GrantMode_GRANT_MODE_NEVER,
}

// grantRank orders modes from narrowest to widest.
func grantRank(m typesv1.GrantMode) int {
	switch m {
	case typesv1.GrantMode_GRANT_MODE_NEVER:
		return 0
	case typesv1.GrantMode_GRANT_MODE_ASK:
		return 1
	case typesv1.GrantMode_GRANT_MODE_ALLOW:
		return 2
	}
	return 3 // unknown: wider than anything, so never accepted
}

func grantModeWord(m typesv1.GrantMode) string {
	for w, gm := range grantModes {
		if gm == m {
			return w
		}
	}
	return m.String()
}

func cloneGrant(g *typesv1.AgentGrant) *typesv1.AgentGrant {
	return &typesv1.AgentGrant{
		Panes:    g.GetPanes(),
		MaxPanes: g.GetMaxPanes(),
		Message:  slices.Clone(g.GetMessage()),
		AskOwner: g.GetAskOwner(),
		Packages: g.GetPackages(),
	}
}

// applyOverride reads the agent's personal override, if there is one, and
// applies it to a only when it has no problems: a refused override changes
// nothing, so it can never widen what's in effect.
func applyOverride(a *TeamAgent, src TeamSource, limits TeamLimits, p *problems) {
	if src.OverridesDir == "" {
		return
	}
	name := a.ID + ".md"
	file := filepath.Join(src.OverridesDir, src.WorkspaceID, name)
	fi, err := os.Lstat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	op := problems{who: a.ID + "'s override"}
	if err != nil {
		op.addf("%s can't be read: %v.", name, pathErrText(err))
		p.lines = append(p.lines, op.lines...)
		return
	}
	content, ok := readAgentFile(file, name, fi.Mode().Type(), &op)
	var o *AgentSpec
	if ok {
		o = buildAgentSpec(content, a.ID, true, limits, &op)
	}
	if o == nil {
		p.lines = append(p.lines, op.lines...)
		return
	}

	base := a.Grant
	widerf := func(key, got, was string) {
		op.addf("grant.%s is %s, wider than the repository file's %s; an override may only narrow a grant.", key, got, was)
	}
	og := o.Grant
	if o.grant.panes && grantRank(og.GetPanes()) > grantRank(base.GetPanes()) {
		widerf("panes", grantModeWord(og.GetPanes()), grantModeWord(base.GetPanes()))
	}
	if o.grant.maxPanes && og.GetMaxPanes() > base.GetMaxPanes() {
		widerf("max_panes", strconv.Itoa(int(og.GetMaxPanes())), strconv.Itoa(int(base.GetMaxPanes())))
	}
	if o.grant.message {
		for _, m := range og.GetMessage() {
			if !slices.Contains(base.GetMessage(), m) {
				widerf("message", "["+strings.Join(og.GetMessage(), ", ")+"]", "["+strings.Join(base.GetMessage(), ", ")+"]")
				break
			}
		}
	}
	if o.grant.askOwner && og.GetAskOwner() && !base.GetAskOwner() {
		widerf("ask_owner", "true", "false")
	}
	if o.grant.packages && grantRank(og.GetPackages()) > grantRank(base.GetPackages()) {
		widerf("packages", grantModeWord(og.GetPackages()), grantModeWord(base.GetPackages()))
	}

	runnerLine, runner, remote, via := a.RunnerLine, a.Runner, a.Remote, a.Via
	if o.set["runner"] {
		runnerLine, runner, remote, via = o.RunnerLine, o.Runner, o.Remote, ""
	}
	if o.set["via"] {
		via = o.Via
	}
	if o.set["runner"] || o.set["via"] {
		checkVia(true, remote, via != "" || o.set["via"], &op)
	}

	if len(op.lines) > 0 {
		p.lines = append(p.lines, op.lines...)
		return
	}
	a.Override = true
	a.RunnerLine, a.Runner, a.Remote, a.Via = runnerLine, runner, remote, via
	if o.set["avatar"] {
		a.Avatar = o.Avatar
	}
	if o.set["share_login"] {
		a.ShareLogin = o.ShareLogin
	}
	if o.grant.panes {
		a.Grant.Panes = og.GetPanes()
	}
	if o.grant.maxPanes {
		a.Grant.MaxPanes = og.GetMaxPanes()
	}
	if o.grant.message {
		a.Grant.Message = slices.Clone(og.GetMessage())
	}
	if o.grant.askOwner {
		a.Grant.AskOwner = og.GetAskOwner()
	}
	if o.grant.packages {
		a.Grant.Packages = og.GetPackages()
	}
}
