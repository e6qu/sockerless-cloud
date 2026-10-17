package placement

import "testing"

func TestParseTableRoutesServicesAndQualifiers(t *testing.T) {
	table, err := ParseTable(" ec2=microvm, ecs:FARGATE = microvm ,rds=container")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		service Service
		want    Kind
	}{
		{Service{Name: "ec2"}, MicroVM},
		{Service{Name: "ecs", Qualifier: "FARGATE"}, MicroVM},
		{Service{Name: "ecs", Qualifier: "EC2"}, Container},
		{Service{Name: "ecs"}, Container},
		{Service{Name: "rds", Qualifier: "db.t3.micro"}, Container},
		{Service{Name: "lambda"}, Container},
	} {
		if got := table.KindFor(tc.service); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.service, got, tc.want)
		}
	}
	if got := table.MicroVMServices(); len(got) != 2 || got[0] != "ec2" || got[1] != "ecs:FARGATE" {
		t.Errorf("microvm services %v", got)
	}
	table.Register(ContainerBackend{})
	if _, err := table.For(Service{Name: "ec2"}); err == nil {
		t.Error("a service configured onto an unregistered backend placed on nothing")
	}
	if b, err := table.For(Service{Name: "lambda"}); err != nil || b.Kind() != Container {
		t.Errorf("lambda placed on %v, %v", b, err)
	}
}

func TestParseTableRefusesWhatItCannotRead(t *testing.T) {
	for _, spec := range []string{"ec2", "ec2=vm", "=microvm"} {
		if _, err := ParseTable(spec); err == nil {
			t.Errorf("%q parsed", spec)
		}
	}
}
