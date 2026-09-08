package main

import (
	"net"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"

	"golang.org/x/net/dns/dnsmessage"
)

// stubWorkloadHostAliasEntries supplies the entries a containerized simulator
// computes, so the resolver is exercised on a developer machine and on a CI
// runner, neither of which runs the simulator inside a container.
func stubWorkloadHostAliasEntries(t *testing.T, gateway string) func() {
	t.Helper()
	previous := workloadHostAliasEntries
	workloadHostAliasEntries = func() []sim.HostEntry {
		return []sim.HostEntry{
			{IP: gateway, Name: "host.docker.internal"},
			{IP: gateway, Name: "host.containers.internal"},
		}
	}
	return func() { workloadHostAliasEntries = previous }
}

func hostAliasQuery(t *testing.T, name string, qType dnsmessage.Type) []byte {
	t.Helper()
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x2a, RecursionDesired: true})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		t.Fatalf("start questions: %v", err)
	}
	if err := builder.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  qType,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("question: %v", err)
	}
	query, err := builder.Finish()
	if err != nil {
		t.Fatalf("finish query: %v", err)
	}
	return query
}

// A workload in the netns tier cannot be given /etc/hosts entries — the runtime
// refuses to create a container that joins another container's namespace with
// host entries — so the container-host alias has to come back from the resolver
// its namespace already points at. Without that, an ECS Dev Desktop workspace
// resolved nothing for the address its control-plane callbacks were configured
// with, and its SSH authorization and idle-agent heartbeats failed together
// while everything reached through the simulator's own API kept working.
func TestWorkloadHostAliasResolvesToTheGatewayEveryOtherTierGets(t *testing.T) {
	gateway := net.IPv4(172, 17, 0, 1).To4()
	restore := stubWorkloadHostAliasEntries(t, gateway.String())
	defer restore()

	for _, alias := range []string{"host.docker.internal.", "host.containers.internal."} {
		response, err := answerRoute53DNS(hostAliasQuery(t, alias, dnsmessage.TypeA))
		if err != nil {
			t.Fatalf("answer %s: %v", alias, err)
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(response)
		if err != nil {
			t.Fatalf("parse response for %s: %v", alias, err)
		}
		if header.RCode != dnsmessage.RCodeSuccess {
			t.Fatalf("%s answered %v, want NOERROR", alias, header.RCode)
		}
		if err := parser.SkipAllQuestions(); err != nil {
			t.Fatalf("skip questions for %s: %v", alias, err)
		}
		answer, err := parser.AnswerHeader()
		if err != nil {
			t.Fatalf("%s returned no answer record: %v", alias, err)
		}
		if answer.Type != dnsmessage.TypeA {
			t.Fatalf("%s answered record type %v, want A", alias, answer.Type)
		}
		body, err := parser.AResource()
		if err != nil {
			t.Fatalf("parse A record for %s: %v", alias, err)
		}
		if got := net.IP(body.A[:]).String(); got != gateway.String() {
			t.Fatalf("%s resolved to %s, want the gateway %s every other tier is given", alias, got, gateway)
		}
	}
}

// The alias is a name the simulator owns, not a hosted zone: a question for a
// record type it has no answer for must come back NOERROR with no records,
// which says the name exists, rather than NXDOMAIN, which denies it.
func TestWorkloadHostAliasDoesNotDenyTheNameForOtherRecordTypes(t *testing.T) {
	restore := stubWorkloadHostAliasEntries(t, "172.17.0.1")
	defer restore()
	response, err := answerRoute53DNS(hostAliasQuery(t, "host.docker.internal.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("answer AAAA: %v", err)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		t.Fatalf("parse AAAA response: %v", err)
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("AAAA answered %v, want NOERROR", header.RCode)
	}
}
