package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestECRWorkloadRegistryAuthMintsATokenForTheSimulatorRegistry(t *testing.T) {
	ecrAuthorizationTokens = sim.MakeStore[ECRAuthorizationToken](nil, "ecr_authorization_tokens")
	previous := simListenAddr
	simListenAddr = ":4566"
	t.Cleanup(func() { simListenAddr = previous })

	credential, err := ecrWorkloadRegistryAuth("127.0.0.1:4566/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.URLEncoding.DecodeString(credential)
	if err != nil {
		t.Fatalf("credential %q is not base64url: %v", credential, err)
	}
	var login struct{ Username, Password string }
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatal(err)
	}
	if login.Username != ecrDockerLoginUsername {
		t.Fatalf("username %q, want %q", login.Username, ecrDockerLoginUsername)
	}
	if _, issued := ecrAuthorizationTokens.Get(login.Password); !issued {
		t.Fatal("the credential's password is not a token the registry issued")
	}

	credential, err = ecrWorkloadRegistryAuth("public.ecr.aws/docker/library/busybox:latest")
	if err != nil || credential != "" {
		t.Fatalf("another registry: credential %q err %v, want an anonymous pull", credential, err)
	}
}

func TestECRWorkloadRegistryAuthFailsWithoutTheListenPort(t *testing.T) {
	previous := simListenAddr
	simListenAddr = "127.0.0.1:notaport"
	t.Cleanup(func() { simListenAddr = previous })

	credential, err := ecrWorkloadRegistryAuth("127.0.0.1:4566/app:latest")
	if err == nil || credential != "" {
		t.Fatalf("credential %q err %v, want no credential and an error", credential, err)
	}
	if want := `resolve the registry of image 127.0.0.1:4566/app:latest: invalid simulator listen port "notaport"`; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}

	// The workload start fails with it rather than pulling anonymously.
	handle, err := startLambdaVpcPauseContainer("inv-0001", nil)
	if handle != nil || err == nil || !strings.Contains(err.Error(), `invalid simulator listen port "notaport"`) {
		t.Fatalf("pause container start: handle %v err %v, want the listen-port error", handle, err)
	}
}
