// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

// setTeamDirACL: ACLs are Linux's; outside a box there's no team folder.
func setTeamDirACL(string, int) error { return nil }
