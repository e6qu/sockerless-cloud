package realexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Running a command inside a Firecracker guest.
//
// A guest is a real machine, and the way you run something on a real machine is
// its own remote-access service — not a private side channel invented for the
// simulator. The root filesystem Firecracker publishes already runs OpenSSH,
// socket-activated at boot with its host keys generated, and the guest is
// already reachable over its network interface (that reachability is what the
// boot wait proves). So the host authorizes itself the way an operator does:
// it puts a public key in the guest's authorized_keys before the machine boots,
// exactly as a cloud does with the public keys in a machine's OS profile, and
// then runs commands over SSH.
//
// This is what makes the operations built on it real. An extension that "ran"
// without anything executing in the guest, or a patch assessment answered from
// a table rather than the guest's own package manager, would be a record of
// something that did not happen.
//
// The mechanism is not new to this repository, only newly reusable: the
// Firecracker arithmetic harness (tests/firecracker/run-arithmetic.sh) has
// always reached its guest exactly this way — an ed25519 key generated with
// ssh-keygen, its public half dropped into the guest's authorized_keys before
// the image is packed, and the same client options used here. That harness runs
// on real hardware in CI, which is what establishes the approach; what was
// missing was any way for the simulators to do it, so each operation that needs
// to run something in a guest had nothing to build on.

// guestExecUser is the account the host runs commands as. The published root
// filesystem has no unprivileged user configured, and the operations built on
// this — installing patches, running an extension — are administrative.
const guestExecUser = "root"

// guestKeyFileName is the private key inside the machine's working directory.
// It never leaves the host.
const guestKeyFileName = "guest_access_key"

// GuestExecCapabilities reports whether this host can run commands inside a
// guest. It asks for exactly what that needs — a key generator and an SSH
// client — on top of what booting a guest already required, so a host that can
// boot but not exec says which of the two it is.
func GuestExecCapabilities() CapabilityReport {
	return DetectCapabilities("ssh", "ssh-keygen")
}

// authorizeGuestAccess generates the key pair the host will use to reach this
// machine and installs the public half into the guest's root filesystem before
// it is packed. It returns the path to the private key.
//
// The key is generated per machine rather than shared: a key that outlived one
// machine would let the host into a later, unrelated one, and there is no
// reason to accept that when generating a fresh pair costs nothing.
func authorizeGuestAccess(ctx context.Context, rootfsDir, workDir string) (string, error) {
	keyPath := filepath.Join(workDir, guestKeyFileName)
	// ssh-keygen refuses to overwrite, and it writes both halves in exactly the
	// formats sshd and the client expect — which is why the key is generated
	// with the tool that owns those formats rather than assembled by hand.
	_ = os.Remove(keyPath)
	_ = os.Remove(keyPath + ".pub")
	if err := (Runner{}).Run(ctx, "ssh-keygen",
		"-t", "ed25519", "-N", "", "-C", "sockerless-guest-exec", "-f", keyPath); err != nil {
		return "", fmt.Errorf("generate the guest access key: %w", err)
	}
	publicKey, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return "", fmt.Errorf("read the generated guest access key: %w", err)
	}

	// root's home is /root, not /home/root, which is the only account this
	// runs as; a different account would need its home resolved from the
	// guest's passwd file rather than assumed.
	sshDir := filepath.Join(rootfsDir, "root", ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", fmt.Errorf("create the guest's .ssh directory: %w", err)
	}
	// sshd refuses to read an authorized_keys file that is group- or
	// world-writable, and answers with a bare authentication failure when it
	// does — so the mode is part of the contract, not tidiness.
	authorized := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authorized, publicKey, 0o600); err != nil {
		return "", fmt.Errorf("authorize the host's key in the guest: %w", err)
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		return "", fmt.Errorf("set the guest's .ssh directory mode: %w", err)
	}
	return keyPath, nil
}

// GuestExecResult is what a command left behind in the guest.
type GuestExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Exec runs a command inside the guest and returns what it produced. A command
// that runs and fails is a result with a non-zero exit code, not an error;
// error is reserved for not being able to run it at all, so a caller can tell
// "the guest said no" from "the guest could not be reached".
func (v *FirecrackerVM) Exec(ctx context.Context, command ...string) (GuestExecResult, error) {
	if len(command) == 0 {
		return GuestExecResult{}, fmt.Errorf("running a command in guest %s requires a command", v.ID)
	}
	if v.accessKeyPath == "" {
		return GuestExecResult{}, fmt.Errorf(
			"guest %s was started without host access authorized, so no command can run in it", v.ID)
	}

	// ssh joins the remaining arguments with spaces and hands the result to the
	// guest's login shell, so an argv passed through as separate words is
	// reassembled by that shell rather than preserved. Quoting each word makes
	// the shell rebuild the argv that was asked for: without it, Exec("/bin/sh",
	// "-c", "echo hello") arrives as `/bin/sh -c echo hello`, which runs echo
	// with no arguments and exits 0 — a command that appears to have succeeded
	// while doing nothing.
	args := append(v.guestSSHArgs(), shellQuoteAll(command))
	cmd := exec.CommandContext(ctx, "ip", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := GuestExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return result, fmt.Errorf("run %v in guest %s: %w", command, v.ID, err)
	}
	// The SSH client reports its own failures — the guest unreachable, the key
	// refused — as exit 255, which is distinguishable from the command's own
	// status because a command exiting 255 would have had to run first.
	if exitErr.ExitCode() == 255 {
		return result, fmt.Errorf("reach guest %s to run %v: %s",
			v.ID, command, bytes.TrimSpace(stderr.Bytes()))
	}
	result.ExitCode = exitErr.ExitCode()
	return result, nil
}

// guestSSHArgs renders the `ip netns exec … ssh …` invocation. The guest's
// address is reachable only inside the network namespace its interface lives
// in, which is the same reason the boot wait pings from there.
func (v *FirecrackerVM) guestSSHArgs() []string {
	return []string{
		"netns", "exec", v.namespace,
		"ssh",
		"-i", v.accessKeyPath,
		// The host authenticates with the key it authorized and nothing else:
		// no password prompt can hang a non-interactive caller.
		"-o", "BatchMode=yes",
		// Each machine generates its own host keys, so there is no prior
		// knowledge of them to check against and nothing to persist between
		// machines that reuse an address.
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		// -- terminates ssh's own options and must come BEFORE the destination.
		// After it, ssh has already taken the destination and everything left is
		// the remote command, so a trailing -- becomes the first word the guest
		// tries to run instead of protecting the command from option parsing.
		"--",
		guestExecUser + "@" + v.PrivateIP.String(),
	}
}

// WaitForGuestExec waits until a command can run in the guest. Its SSH service
// is socket-activated, so it answers once the machine has finished booting far
// enough to accept a connection — which is a little after the machine answers
// a ping. Callers that intend to run something wait here rather than treating
// the first failure as the guest being broken.
func (v *FirecrackerVM) WaitForGuestExec(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	var lastErr error
	for {
		if _, err := v.Exec(ctx, "true"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if !v.Alive() {
			return fmt.Errorf("guest %s stopped before it accepted a command: %w", v.ID, lastErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest %s did not accept a command within %s: %w", v.ID, within, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// shellQuoteAll renders an argv as one command string the guest's shell will
// split back into exactly those words.
func shellQuoteAll(command []string) string {
	quoted := make([]string, 0, len(command))
	for _, word := range command {
		quoted = append(quoted, "'"+strings.ReplaceAll(word, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
