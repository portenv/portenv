// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"slices"
)

// POSIX ACLs as Linux keeps them, in the system.posix_acl_access and
// system.posix_acl_default extended attributes: a version, then one
// (tag, perm, id) entry per line of getfacl. open-project sets them through
// a descriptor of each file (acl_linux.go) instead of running setfacl over
// paths, so it never follows a link an agent puts in the tree.

const (
	aclVersion     = 2
	aclUserObj     = 0x01
	aclUser        = 0x02
	aclGroupObj    = 0x04
	aclGroup       = 0x08
	aclMask        = 0x10
	aclOther       = 0x20
	aclUndefinedID = 0xffffffff
)

type aclEntry struct {
	tag  uint16
	perm uint16
	id   uint32
}

var errBadACL = errors.New("an ACL this helper can't read")

func decodeACL(b []byte) ([]aclEntry, error) {
	if len(b) < 4 || (len(b)-4)%8 != 0 || binary.LittleEndian.Uint32(b) != aclVersion {
		return nil, errBadACL
	}
	var es []aclEntry
	for b = b[4:]; len(b) > 0; b = b[8:] {
		es = append(es, aclEntry{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:]), binary.LittleEndian.Uint32(b[4:])})
	}
	return es, nil
}

// encodeACL writes the entries in the order the kernel requires: by tag,
// then by id.
func encodeACL(es []aclEntry) []byte {
	es = slices.Clone(es)
	slices.SortFunc(es, func(a, b aclEntry) int {
		if a.tag != b.tag {
			return int(a.tag) - int(b.tag)
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	b := binary.LittleEndian.AppendUint32(nil, aclVersion)
	for _, e := range es {
		b = binary.LittleEndian.AppendUint16(b, e.tag)
		b = binary.LittleEndian.AppendUint16(b, e.perm)
		b = binary.LittleEndian.AppendUint32(b, e.id)
	}
	return b
}

// minimalACL is the ACL a mode alone means (a file without an ACL).
func minimalACL(mode fs.FileMode) []aclEntry {
	p := uint16(mode.Perm()) // #nosec G115 -- nine bits
	return []aclEntry{{aclUserObj, p >> 6 & 7, aclUndefinedID}, {aclGroupObj, p >> 3 & 7, aclUndefinedID}, {aclOther, p & 7, aclUndefinedID}}
}

// aclBase is an ACL's owner, owning group and other entries, which is
// what setfacl starts a new default ACL from.
func aclBase(es []aclEntry) []aclEntry {
	var out []aclEntry
	for _, e := range es {
		if e.tag == aclUserObj || e.tag == aclGroupObj || e.tag == aclOther {
			out = append(out, e)
		}
	}
	return out
}

// aclPerm is the perm of the first entry with the tag.
func aclPerm(es []aclEntry, tag uint16) (uint16, bool) {
	for _, e := range es {
		if e.tag == tag {
			return e.perm, true
		}
	}
	return 0, false
}

// aclWithGroup is setfacl -m g:<gid>:<perm>: the named group's entry set
// (added or replaced), and the mask recomputed as the union of the group
// class, so the entry is effective.
func aclWithGroup(es []aclEntry, gid uint32, perm uint16) []aclEntry {
	out := make([]aclEntry, 0, len(es)+2)
	for _, e := range es {
		if (e.tag == aclGroup && e.id == gid) || e.tag == aclMask {
			continue
		}
		out = append(out, e)
	}
	out = append(out, aclEntry{aclGroup, perm & 7, gid})
	var mask uint16
	for _, e := range out {
		if e.tag == aclGroupObj || e.tag == aclUser || e.tag == aclGroup {
			mask |= e.perm
		}
	}
	return append(out, aclEntry{aclMask, mask, aclUndefinedID})
}

// readOnlyBase is an ACL's owner, owning group and other entries, with
// others' write taken off, and the owning group's when gid (the file's
// group) isn't ownGID.
func readOnlyBase(es []aclEntry, gid, ownGID uint32) []aclEntry {
	out := aclBase(es)
	for i := range out {
		switch {
		case out[i].tag == aclOther, out[i].tag == aclGroupObj && gid != ownGID:
			out[i].perm &^= 2
		}
	}
	return out
}

// aclLetsOthersWrite is whether the ACL of a file owned by uid and gid
// lets a uid other than uid write: others' write, the owning group's when
// gid isn't ownGID, or a named user or group entry (other than uid or
// ownGID) with write that the mask lets through.
func aclLetsOthersWrite(es []aclEntry, uid, gid, ownGID uint32) bool {
	mask, ok := aclPerm(es, aclMask)
	if !ok {
		mask = 7
	}
	for _, e := range es {
		w := e.perm&2 != 0
		switch e.tag {
		case aclOther:
			if w {
				return true
			}
		case aclGroupObj:
			if w && mask&2 != 0 && gid != ownGID {
				return true
			}
		case aclUser:
			if w && mask&2 != 0 && e.id != uid {
				return true
			}
		case aclGroup:
			if w && mask&2 != 0 && e.id != ownGID {
				return true
			}
		}
	}
	return false
}

// aclWithUsers is the ACL with its named-user entries for uids in [lo, hi]
// replaced by want (uid → perm), others kept, and the mask the union of
// the group class (none when no named entry is left, so the ACL is the
// mode's again).
func aclWithUsers(es []aclEntry, want map[uint32]uint16, lo, hi uint32) []aclEntry {
	out := make([]aclEntry, 0, len(es)+len(want))
	named := false
	for _, e := range es {
		switch {
		case e.tag == aclMask:
			continue
		case e.tag == aclUser && e.id >= lo && e.id <= hi:
			continue
		case e.tag == aclUser || e.tag == aclGroup:
			named = true
		}
		out = append(out, e)
	}
	for uid, perm := range want {
		out = append(out, aclEntry{aclUser, perm & 7, uid})
		named = true
	}
	if !named {
		return out
	}
	var mask uint16
	for _, e := range out {
		if e.tag == aclGroupObj || e.tag == aclUser || e.tag == aclGroup {
			mask |= e.perm
		}
	}
	return append(out, aclEntry{aclMask, mask, aclUndefinedID})
}

// aclHasNamed is whether the ACL has a named user or group entry.
func aclHasNamed(es []aclEntry) bool {
	for _, e := range es {
		if e.tag == aclUser || e.tag == aclGroup {
			return true
		}
	}
	return false
}
