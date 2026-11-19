package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// SSH connection multiplexing: one master connection is opened up front and
// every later ssh call reuses it, which saves the TCP/key exchange and the
// authentication of each of the many short remote commands.

var (
	muxDir    string
	muxTarget string
	muxArgs   []string
)

// sshCommand builds an ssh command that goes through the master connection
// when one is up. Without it the command is a plain ssh call.
func sshCommand(args ...string) *exec.Cmd {
	return exec.Command("ssh", append(append([]string{}, muxArgs...), args...)...)
}

// startMux opens the master connection. Any failure leaves multiplexing off
// and every call falls back to its own connection.
func startMux(remote *RemoteConfig) {
	dir, err := os.MkdirTemp("", "grs-")
	if err != nil {
		return
	}
	sock := filepath.Join(dir, "%C")
	master := exec.Command("ssh", "-fN", "-o", "ControlMaster=yes", "-o", "ControlPath="+sock,
		"-o", "ControlPersist=120", getSSHTarget(remote))
	logCommand("L", "ssh -fN -o ControlMaster=yes (connection sharing)")
	if err := master.Run(); err != nil {
		os.RemoveAll(dir)
		return
	}
	muxDir = dir
	muxTarget = getSSHTarget(remote)
	muxArgs = []string{"-o", "ControlMaster=no", "-o", "ControlPath=" + sock}
}

// stopMux closes the master connection and removes its socket directory.
func stopMux() {
	if muxDir == "" {
		return
	}
	exec.Command("ssh", append(append([]string{}, muxArgs...), "-O", "exit", muxTarget)...).Run()
	os.RemoveAll(muxDir)
	muxDir, muxArgs = "", nil
}
