package realexec

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The half of guest execution that needs no booted machine: authorizing the
// host into a root filesystem before it is packed, and the invocation the host
// then makes. Both are asserted here because a host without nested KVM cannot
// boot a guest, and leaving them untested there would mean the only coverage
// lived somewhere most contributors cannot run.

func TestAuthorizeGuestAccessInstallsAUsableKey(t *testing.T) {
	requireKeyGenerator(t)
	rootfs := t.TempDir()
	workDir := t.TempDir()

	keyPath, err := authorizeGuestAccess(context.Background(), rootfs, workDir)
	if err != nil {
		t.Fatalf("authorizeGuestAccess: %v", err)
	}

	// The private half stays on the host.
	if filepath.Dir(keyPath) != workDir {
		t.Errorf("private key at %s, want it inside the machine's working directory %s", keyPath, workDir)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("private key was not written: %v", err)
	}

	// The public half is authorized in the guest, in the format sshd reads.
	authorized := filepath.Join(rootfs, "root", ".ssh", "authorized_keys")
	content, err := os.ReadFile(authorized)
	if err != nil {
		t.Fatalf("authorized_keys was not installed: %v", err)
	}
	if !strings.HasPrefix(string(content), "ssh-ed25519 ") {
		t.Errorf("authorized_keys does not hold an ed25519 public key: %q", string(content))
	}
	public, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read the generated public key: %v", err)
	}
	if string(content) != string(public) {
		t.Error("the key authorized in the guest is not the one the host holds the private half of")
	}

	// sshd refuses an authorized_keys file, or a .ssh directory, that others can
	// write — and answers with a bare authentication failure when it does, which
	// is why the modes are asserted rather than assumed.
	info, err := os.Stat(authorized)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("authorized_keys mode is %o, which sshd refuses to read", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(authorized))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf(".ssh directory mode is %o, which sshd refuses to read through", mode)
	}
}

// Each machine gets its own key, so the host cannot reach a later machine with
// an earlier machine's credential.
func TestAuthorizeGuestAccessIssuesADistinctKeyPerMachine(t *testing.T) {
	requireKeyGenerator(t)
	read := func(t *testing.T) string {
		t.Helper()
		rootfs, workDir := t.TempDir(), t.TempDir()
		if _, err := authorizeGuestAccess(context.Background(), rootfs, workDir); err != nil {
			t.Fatalf("authorizeGuestAccess: %v", err)
		}
		content, err := os.ReadFile(filepath.Join(rootfs, "root", ".ssh", "authorized_keys"))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	first, second := read(t), read(t)
	if first == second {
		t.Fatal("two machines were authorized with the same key")
	}
}

// The invocation reaches the guest through the namespace its address lives in,
// authenticates only with the key that was authorized, and cannot stop to ask a
// human anything.
func TestGuestSSHInvocation(t *testing.T) {
	vm := &FirecrackerVM{
		ID:            "machine-1",
		namespace:     "vpc-ns",
		accessKeyPath: "/work/guest_access_key",
		PrivateIP:     net.ParseIP("10.0.0.7"),
	}
	args := strings.Join(vm.guestSSHArgs(), " ")

	for _, want := range []string{
		"netns exec vpc-ns",               // the guest is only reachable from there
		"ssh",                             // over the machine's own remote access
		"-i /work/guest_access_key",       // with the key authorized in it
		"-o BatchMode=yes",                // never prompting
		"-o StrictHostKeyChecking=no",     // no prior knowledge of a per-machine host key
		"-o UserKnownHostsFile=/dev/null", // and nothing persisted between machines
		"root@10.0.0.7",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("invocation %q is missing %q", args, want)
		}
	}
	// -- terminates ssh's own options and must precede the destination. After
	// the destination ssh is already reading the remote command, so a trailing
	// -- becomes the first word the guest tries to run — which is a command
	// that silently produces nothing rather than the one that was asked for.
	if !strings.HasSuffix(args, "-- root@10.0.0.7") {
		t.Errorf("invocation %q must end with the destination, with -- before it", args)
	}
}

// A machine started without host access says so rather than failing obscurely
// at connect time.
func TestExecRefusesAMachineWithNoAuthorizedAccess(t *testing.T) {
	vm := &FirecrackerVM{ID: "machine-2", namespace: "vpc-ns"}
	if _, err := vm.Exec(context.Background(), "true"); err == nil {
		t.Fatal("a machine with no authorized access accepted a command")
	}
	if _, err := vm.Exec(context.Background()); err == nil {
		t.Fatal("an empty command was accepted")
	}
}

// requireKeyGenerator fails loudly rather than skipping. Authorizing the host
// into a root filesystem is generating a key pair and writing a file, which
// every platform can do — unlike reaching the guest afterwards, which needs the
// network namespace its address lives in. Conflating the two would gate a
// portable operation behind Linux and leave it untested everywhere else.
func requireKeyGenerator(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Fatalf("ssh-keygen is required to authorize a guest and ships with every supported host: %v", err)
	}
}

// ssh joins the remaining arguments with spaces and hands the result to the
// guest's login shell, so an argv passed as separate words is reassembled by
// that shell instead of preserved. Quoting is what makes the shell rebuild the
// argv that was asked for.
//
// Getting this wrong is silent: Exec("/bin/sh", "-c", "echo hello") arriving as
// `/bin/sh -c echo hello` runs echo with no arguments and exits 0, so the
// command appears to have succeeded while producing nothing. Verified against a
// real sshd — the unquoted form printed an empty line and exited 0, the quoted
// form printed the output and carried the exit status back.
func TestGuestCommandSurvivesSSHRejoiningIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		want    string
	}{
		{
			"a command with arguments stays one word each",
			[]string{"/bin/sh", "-c", "echo hello"},
			`'/bin/sh' '-c' 'echo hello'`,
		},
		{
			"a word carrying spaces is not split",
			[]string{"echo", "two words"},
			`'echo' 'two words'`,
		},
		{
			"a single quote inside a word does not end the quoting",
			[]string{"echo", "it's"},
			`'echo' 'it'\''s'`,
		},
		{
			"a word that looks like an option is not read as one",
			[]string{"echo", "--help"},
			`'echo' '--help'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellQuoteAll(tc.command); got != tc.want {
				t.Errorf("shellQuoteAll(%q) = %s, want %s", tc.command, got, tc.want)
			}
		})
	}
}
