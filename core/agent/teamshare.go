// SPDX-License-Identifier: Apache-2.0

package agent

// The team folder, /home/team (ADR 0022 §2, CC-0706 item 19): every agent
// and the owner read and write what's in it. Its own default ACL covers
// what's made in it with a usual umask, but not a file made 0600 (the
// mask comes from the mode it's made with), nor one renamed in from
// elsewhere, which keeps its own mode, group and ACL. So init, as root,
// shares every agent's file and folder in it as it appears: group team,
// the group's read and write, and work's own named entry (which holds
// even in a shell of work's started before work joined group team).

// teamSharePerm is rw-, or rwx for a folder or a program.
func teamSharePerm(dir, exec bool) uint16 {
	if dir || exec {
		return 7
	}
	return 6
}

// teamShareAccess is cur with the owning group given at least perm, work's
// named entry set to perm, and the mask the union of the group class.
// Other entries stay.
func teamShareAccess(cur []aclEntry, perm uint16, workUID uint32) []aclEntry {
	out := make([]aclEntry, 0, len(cur)+2)
	for _, e := range cur {
		switch {
		case e.tag == aclMask, e.tag == aclUser && e.id == workUID:
			continue
		case e.tag == aclGroupObj:
			e.perm |= perm
		}
		out = append(out, e)
	}
	out = append(out, aclEntry{aclUser, perm, workUID})
	var mask uint16
	for _, e := range out {
		if e.tag == aclGroupObj || e.tag == aclUser || e.tag == aclGroup {
			mask |= e.perm
		}
	}
	return append(out, aclEntry{aclMask, mask, aclUndefinedID})
}

// teamShareDefault is a team folder's default ACL: what's made in it is
// its maker's, work's and the team's to read and write, no one else's.
func teamShareDefault(workUID uint32) []aclEntry {
	return []aclEntry{
		{aclUserObj, 7, aclUndefinedID}, {aclUser, 7, workUID}, {aclGroupObj, 7, aclUndefinedID},
		{aclMask, 7, aclUndefinedID}, {aclOther, 0, aclUndefinedID},
	}
}
