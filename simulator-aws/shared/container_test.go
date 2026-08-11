package simulator

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveLocalImage_AR(t *testing.T) {
	got := ResolveLocalImage("us-central1-docker.pkg.dev/proj/docker-hub/library/alpine:latest")
	if got != "alpine:latest" {
		t.Errorf("expected alpine:latest, got %q", got)
	}
}

func TestResolveLocalImage_ECR(t *testing.T) {
	got := ResolveLocalImage("123456789012.dkr.ecr.us-east-1.amazonaws.com/alpine:latest")
	if got != "alpine:latest" {
		t.Errorf("expected alpine:latest, got %q", got)
	}
}

func TestResolveLocalImage_ACR(t *testing.T) {
	got := ResolveLocalImage("myacr.azurecr.io/library/nginx:latest")
	if got != "nginx:latest" {
		t.Errorf("expected nginx:latest, got %q", got)
	}
}

func TestResolveLocalImage_Passthrough(t *testing.T) {
	got := ResolveLocalImage("alpine:latest")
	if got != "alpine:latest" {
		t.Errorf("expected alpine:latest, got %q", got)
	}
}

func TestResolveLocalImage_AmazonECRPublicPassthrough(t *testing.T) {
	const image = "public.ecr.aws/docker/library/alpine:3.21"
	if got := ResolveLocalImage(image); got != image {
		t.Errorf("expected Amazon ECR Public coordinate %q, got %q", image, got)
	}
}

func TestResolveLocalImage_ECR_DockerHub(t *testing.T) {
	// ECR pull-through cache hit for a Docker Hub library image:
	// `docker-hub/` and `library/` both get stripped so the resolved
	// ref matches the plain Docker Hub name the local daemon can pull.
	got := ResolveLocalImage("123456789012.dkr.ecr.us-east-1.amazonaws.com/docker-hub/library/nginx:1.25")
	if got != "nginx:1.25" {
		t.Errorf("expected nginx:1.25, got %q", got)
	}
}

func TestResolveLocalImage_ECR_DockerHubNonLibrary(t *testing.T) {
	// Non-library docker-hub image: strip docker-hub/ but leave the
	// user/repo path intact so e.g. `user/myimg:tag` round-trips.
	got := ResolveLocalImage("123456789012.dkr.ecr.us-east-1.amazonaws.com/docker-hub/user/myimg:tag")
	if got != "user/myimg:tag" {
		t.Errorf("expected user/myimg:tag, got %q", got)
	}
}

func TestResolveLocalImage_ECR_LibraryOnly(t *testing.T) {
	got := ResolveLocalImage("123456789012.dkr.ecr.us-east-1.amazonaws.com/library/nginx:1.25")
	if got != "nginx:1.25" {
		t.Errorf("expected nginx:1.25, got %q", got)
	}
}

// parsePlatform — workload arch is carried in the spec, never derived
// from the host. Empty or malformed → error (no silent fallback).
func TestParsePlatform(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"linux/arm64", "linux/arm64", false},
		{"linux/amd64", "linux/amd64", false},
		{"linux/arm/v7", "linux/arm/v7", false},
		{"", "", true},        // empty: required field
		{"garbage", "", true}, // not "os/arch[/variant]"
	}
	for _, tc := range cases {
		got, err := parsePlatform(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePlatform(%q) = no err, want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePlatform(%q) err = %v, want nil", tc.in, err)
			continue
		}
		flat := got.OS + "/" + got.Architecture
		if got.Variant != "" {
			flat += "/" + got.Variant
		}
		if flat != tc.want {
			t.Errorf("parsePlatform(%q) = %s, want %s", tc.in, flat, tc.want)
		}
	}
}

// The exact wire text Docker returns when it wires a container onto a bridge
// network on a kernel whose netfilter build omits the table the direct-access
// filtering rule needs.
const dockerMissingRawTableError = `Error response from daemon: failed to set up container networking: ` +
	`failed to create endpoint sockerless-sim-aws-task-0123456789ab on network bridge: ` +
	`Unable to enable DIRECT ACCESS FILTERING - DROP rule:  (iptables failed: ` +
	"iptables --wait -t raw -A PREROUTING -d 172.17.0.2 ! -i docker0 -j DROP: " +
	"can't initialize iptables table `raw': Table does not exist (do you need to insmod?))"

func TestMissingNetfilterTableHint_NamesTheKernelDependency(t *testing.T) {
	hint := MissingNetfilterTableHint(errors.New(dockerMissingRawTableError))
	if hint == "" {
		t.Fatal("a missing netfilter table must produce an actionable hint")
	}
	for _, want := range []string{`"raw"`, "iptable_raw", "CONFIG_IP_NF_RAW"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not name %q", hint, want)
		}
	}
}

func TestMissingNetfilterTableHint_IgnoresUnrelatedFailures(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("Error response from daemon: No such image: alpine:3.20"),
		errors.New("Error response from daemon: driver failed programming external connectivity"),
	} {
		if hint := MissingNetfilterTableHint(err); hint != "" {
			t.Errorf("MissingNetfilterTableHint(%v) = %q, want empty", err, hint)
		}
	}
}

func TestContainerNotFoundErrorRecognizesPodmanCompatibilityResponse(t *testing.T) {
	err := errors.New("Error response from daemon: no container with ID abc123 found in database: no such container")
	if !containerNotFoundError(err) {
		t.Fatalf("containerNotFoundError(%q) = false, want true", err)
	}
}

func TestContainerNotFoundErrorRejectsOtherRuntimeFailures(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("Error response from daemon: permission denied"),
		errors.New("Error response from daemon: no such image"),
	} {
		if containerNotFoundError(err) {
			t.Errorf("containerNotFoundError(%v) = true, want false", err)
		}
	}
}
