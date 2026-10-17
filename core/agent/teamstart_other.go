// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package agent

import "errors"

func writeFileAsUser(int, int, string, []byte) error {
	return errors.New("agents run only inside a workspace")
}

func killAgentUID(uid int) error {
	if err := checkKillableUID(uid); err != nil {
		return err
	}
	return errors.New("agents run only inside a workspace")
}
