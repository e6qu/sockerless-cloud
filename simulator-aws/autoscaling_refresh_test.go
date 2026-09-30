package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// asRefreshBoots holds every VM boot until the test answers it: each boot
// reports its instance on booting and returns what the test sends on results.
func asRefreshBoots(t *testing.T) (booting chan string, results chan error) {
	t.Helper()
	booting = make(chan string, 8)
	results = make(chan error)
	done := make(chan struct{})
	// A test that fails midway leaves boots unanswered; ending them lets the
	// drain its cleanup runs finish.
	t.Cleanup(func() { close(done) })
	ec2BootInstance = func(_ context.Context, inst EC2Instance) error {
		booting <- inst.InstanceId
		select {
		case err := <-results:
			return err
		case <-done:
			return errors.New("test ended")
		}
	}
	return booting, results
}

// asRefreshWatch follows the statuses stored for each refresh, so a test waits
// for the status that ends a chain of launches, warmups and steps instead of
// draining, which would drop the chain's later links.
type asRefreshWatch struct {
	mu     sync.Mutex
	cond   *sync.Cond
	status map[string]string
}

func asWatchRefreshes(t *testing.T) *asRefreshWatch {
	t.Helper()
	w := &asRefreshWatch{status: map[string]string{}}
	w.cond = sync.NewCond(&w.mu)
	prev := asRefreshStored
	asRefreshStored = func(ref ASInstanceRefresh) {
		w.mu.Lock()
		w.status[ref.InstanceRefreshId] = ref.Status
		w.cond.Broadcast()
		w.mu.Unlock()
	}
	t.Cleanup(func() { asRefreshStored = prev })
	return w
}

func (w *asRefreshWatch) await(id, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.status[id] != status {
		w.cond.Wait()
	}
}

// asRefreshGroup creates a group of n InService members launched from the
// launch configuration "lc", and a launch template "lt-new" whose one version
// launches ami-new.
func asRefreshGroup(t *testing.T, name string, n int, booting chan string, results chan error) []string {
	t.Helper()
	ec2LaunchTemplates.Put("lt-new", EC2LaunchTemplate{
		LaunchTemplateId:     "lt-new",
		LaunchTemplateName:   "new",
		DefaultVersionNumber: 1,
		LatestVersionNumber:  1,
		Versions:             []EC2LaunchTemplateVersion{{VersionNumber: 1, DefaultVersion: true, Data: EC2LaunchTemplateData{ImageId: "ami-new", InstanceType: "t3.small"}}},
	})
	rec := asQuery(t, handleASCreateAutoScalingGroup, url.Values{
		"AutoScalingGroupName":    {name},
		"LaunchConfigurationName": {"lc"},
		"MinSize":                 {"0"},
		"MaxSize":                 {"4"},
		"DesiredCapacity":         {strconv.Itoa(n)},
		"VPCZoneIdentifier":       {"subnet-asg"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateAutoScalingGroup = %d %s", rec.Code, rec.Body)
	}
	for range n {
		<-booting
	}
	for range n {
		results <- nil
	}
	bg.Await()
	asg, _ := autoScalingGroups.Get(name)
	return asg.InstanceIds
}

func asStartRefresh(t *testing.T, form url.Values) string {
	t.Helper()
	rec := asQuery(t, handleASXStartInstanceRefresh, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("StartInstanceRefresh = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	start := strings.Index(body, "<InstanceRefreshId>") + len("<InstanceRefreshId>")
	return body[start:strings.Index(body, "</InstanceRefreshId>")]
}

// asRefreshNow reads a refresh under the lock every change to it holds, so it
// sees the change a boot it just observed was started by.
func asRefreshNow(t *testing.T, id string) (ASInstanceRefresh, AutoScalingGroup) {
	t.Helper()
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref, ok := asInstanceRefreshes.Get(id)
	if !ok {
		t.Fatalf("instance refresh %s not stored", id)
	}
	asg, _ := autoScalingGroups.Get(ref.AutoScalingGroupName)
	return ref, asg
}

func asWantRefresh(t *testing.T, id, status string, pct, toUpdate int) ASInstanceRefresh {
	t.Helper()
	ref, asg := asRefreshNow(t, id)
	if ref.Status != status || asRefreshPercentage(ref, asg) != pct || asRefreshInstancesToUpdate(ref) != toUpdate {
		t.Fatalf("refresh = %s %d%% %d to update (reason %q), want %s %d%% %d",
			ref.Status, asRefreshPercentage(ref, asg), asRefreshInstancesToUpdate(ref), ref.StatusReason, status, pct, toUpdate)
	}
	return ref
}

func asWantError(t *testing.T, h http.HandlerFunc, form url.Values, code string) {
	t.Helper()
	rec := asQuery(t, h, form)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "<Code>"+code+"</Code>") {
		t.Fatalf("got %d %s, want %s", rec.Code, rec.Body, code)
	}
}

// TestAutoScalingInstanceRefreshReplacesInBatches runs a refresh to a new
// launch template on two members with MinHealthyPercentage 50: it takes one
// member out of service at a time, launches its replacement from the desired
// configuration, and once the refresh succeeds the group launches from it.
func TestAutoScalingInstanceRefreshReplacesInBatches(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	old := asRefreshGroup(t, "rolling", 2, booting, results)

	id := asStartRefresh(t, url.Values{
		"AutoScalingGroupName":                                 {"rolling"},
		"DesiredConfiguration.LaunchTemplate.LaunchTemplateId": {"lt-new"},
		"DesiredConfiguration.LaunchTemplate.Version":          {"1"},
		"Preferences.MinHealthyPercentage":                     {"50"},
		"Preferences.InstanceWarmup":                           {"0"},
	})
	asWantError(t, handleASXStartInstanceRefresh, url.Values{"AutoScalingGroupName": {"rolling"}}, "InstanceRefreshInProgress")

	first := <-booting
	asWantRefresh(t, id, asRefreshInProgress, 0, 2)
	_, asg := asRefreshNow(t, id)
	if len(asg.InstanceIds) != 2 || indexOfString(asg.InstanceIds, first) < 0 {
		t.Fatalf("members during the first batch = %v, want one old member and %s", asg.InstanceIds, first)
	}
	if inst, _ := ec2Instances.Get(old[0]); inst.State != "terminated" {
		t.Fatalf("first replaced member %s is %q, want terminated", old[0], inst.State)
	}
	if inst, _ := ec2Instances.Get(first); inst.ImageId != "ami-new" || inst.InstanceType != "t3.small" {
		t.Fatalf("replacement launched %s/%s, want the launch template's ami-new/t3.small", inst.ImageId, inst.InstanceType)
	}

	results <- nil
	second := <-booting
	asWantRefresh(t, id, asRefreshInProgress, 50, 1)
	if inst, _ := ec2Instances.Get(old[1]); inst.State != "terminated" {
		t.Fatalf("second replaced member %s is %q, want terminated", old[1], inst.State)
	}

	results <- nil
	w.await(id, asRefreshSuccessful)
	ref := asWantRefresh(t, id, asRefreshSuccessful, 100, 0)
	if ref.EndTime == "" {
		t.Fatal("successful refresh has no EndTime")
	}
	asg, _ = autoScalingGroups.Get("rolling")
	if len(asg.InstanceIds) != 2 || asg.InstanceIds[0] != first || asg.InstanceIds[1] != second {
		t.Fatalf("members after the refresh = %v, want [%s %s]", asg.InstanceIds, first, second)
	}
	if asg.LaunchConfigurationName != "" || asg.LaunchTemplate.LaunchTemplateId != "lt-new" || asg.LaunchTemplate.Version != "1" {
		t.Fatalf("group launches from %q / %+v, want lt-new version 1", asg.LaunchConfigurationName, asg.LaunchTemplate)
	}
	xml := autoScalingGroupXML(asg)
	if strings.Contains(xml, "<LaunchConfigurationName>") || !strings.Contains(xml, "<LaunchTemplate><LaunchTemplateId>lt-new</LaunchTemplateId><LaunchTemplateName>new</LaunchTemplateName><Version>1</Version></LaunchTemplate>") {
		t.Fatalf("DescribeAutoScalingGroups member: %s", xml)
	}
	described := asInstanceRefreshXML(ref, asg)
	for _, want := range []string{"<Status>Successful</Status>", "<PercentageComplete>100</PercentageComplete>", "<InstancesToUpdate>0</InstancesToUpdate>",
		"<MinHealthyPercentage>50</MinHealthyPercentage>", "<InstanceWarmup>0</InstanceWarmup>", "<DesiredConfiguration><LaunchTemplate><LaunchTemplateId>lt-new</LaunchTemplateId><Version>1</Version></LaunchTemplate></DesiredConfiguration>"} {
		if !strings.Contains(described, want) {
			t.Fatalf("DescribeInstanceRefreshes member lacks %s: %s", want, described)
		}
	}
	asWantError(t, handleASXRollbackInstanceRefresh, url.Values{"AutoScalingGroupName": {"rolling"}}, "ActiveInstanceRefreshNotFound")
}

// TestAutoScalingInstanceRefreshWaitsOutTheInstanceWarmup shows a refresh
// holds the next batch for the instance warmup once a replacement is
// InService, and counts the replacement only when the warmup ends.
func TestAutoScalingInstanceRefreshWaitsOutTheInstanceWarmup(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	asRefreshGroup(t, "warm", 2, booting, results)

	id := asStartRefresh(t, url.Values{
		"AutoScalingGroupName":             {"warm"},
		"Preferences.MinHealthyPercentage": {"100"},
		"Preferences.InstanceWarmup":       {"3600"},
	})
	replacement := <-booting
	results <- nil
	// The drain cancels the hour-long warmup timer rather than waiting it out,
	// leaving the refresh as the timer found it.
	bg.Await()

	ref := asWantRefresh(t, id, asRefreshInProgress, 0, 2)
	if want := "Waiting for instances to warm up before continuing. For example: " + replacement + " is warming up."; ref.StatusReason != want {
		t.Fatalf("StatusReason = %q, want %q", ref.StatusReason, want)
	}
	if ref.TimerKind != asRefreshTimerWarmup {
		t.Fatalf("refresh timer = %q, want the warmup", ref.TimerKind)
	}
	select {
	case id := <-booting:
		t.Fatalf("launched %s before the warmup ended", id)
	default:
	}
}

func TestAutoScalingInstanceRefreshWarmupPrecedence(t *testing.T) {
	five, zero := 5, 0
	asg := AutoScalingGroup{HealthCheckGracePeriod: 300}
	for _, tc := range []struct {
		name   string
		pref   *int
		groupW *int
		want   int
	}{
		{"health check grace period", nil, nil, 300},
		{"default instance warmup", nil, &five, 5},
		{"zero default instance warmup", nil, &zero, 0},
		{"preference", &zero, &five, 0},
	} {
		asg.DefaultInstanceWarmup = tc.groupW
		ref := ASInstanceRefresh{Preferences: ASRefreshPreferences{InstanceWarmup: tc.pref}}
		if got := asRefreshWarmup(ref, asg).Seconds(); int(got) != tc.want {
			t.Errorf("%s: warmup = %vs, want %ds", tc.name, got, tc.want)
		}
	}
}

func TestAutoScalingInstanceRefreshBatchSize(t *testing.T) {
	pct := func(v int) *int { return &v }
	for _, tc := range []struct {
		min, desired      int
		max               *int
		launch, terminate int
	}{
		{90, 10, nil, 1, 1},
		{100, 4, nil, 1, 1},
		{0, 4, nil, 4, 4},
		{50, 4, nil, 2, 2},
		{100, 10, pct(110), 1, 0},
		{100, 4, pct(100), 1, 0},
		{50, 4, pct(150), 4, 2},
	} {
		launch, terminate := asRefreshBatch(ASRefreshPreferences{MinHealthyPercentage: tc.min, MaxHealthyPercentage: tc.max}, tc.desired)
		if launch != tc.launch || terminate != tc.terminate {
			t.Errorf("min %d max %v desired %d: batch launches %d terminating %d first, want %d and %d",
				tc.min, tc.max, tc.desired, launch, terminate, tc.launch, tc.terminate)
		}
	}
}

// TestAutoScalingInstanceRefreshLaunchesBeforeTerminating shows that with
// MaxHealthyPercentage room above the desired capacity and a minimum that
// keeps every member in service, the refresh launches the replacement first
// and takes the old member out only after the replacement has warmed up.
func TestAutoScalingInstanceRefreshLaunchesBeforeTerminating(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	old := asRefreshGroup(t, "surge", 1, booting, results)

	id := asStartRefresh(t, url.Values{
		"AutoScalingGroupName":             {"surge"},
		"Preferences.MinHealthyPercentage": {"100"},
		"Preferences.MaxHealthyPercentage": {"200"},
		"Preferences.InstanceWarmup":       {"0"},
	})
	replacement := <-booting
	_, asg := asRefreshNow(t, id)
	if len(asg.InstanceIds) != 2 {
		t.Fatalf("members while the replacement boots = %v, want the old member and %s", asg.InstanceIds, replacement)
	}
	if inst, _ := ec2Instances.Get(old[0]); inst.State != "running" {
		t.Fatalf("old member is %q while its replacement boots, want running", inst.State)
	}
	results <- nil
	w.await(id, asRefreshSuccessful)
	asWantRefresh(t, id, asRefreshSuccessful, 100, 0)
	asg, _ = autoScalingGroups.Get("surge")
	if len(asg.InstanceIds) != 1 || asg.InstanceIds[0] != replacement {
		t.Fatalf("members after the refresh = %v, want [%s]", asg.InstanceIds, replacement)
	}
	if inst, _ := ec2Instances.Get(old[0]); inst.State != "terminated" {
		t.Fatalf("old member is %q after the refresh, want terminated", inst.State)
	}
}

// TestAutoScalingInstanceRefreshRollsBackAnInProgressRefresh rolls a refresh
// back while its first replacement boots: the rollback replaces what the
// refresh launched with an instance from the launch configuration the group
// had, which the group keeps.
func TestAutoScalingInstanceRefreshRollsBackAnInProgressRefresh(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	asRefreshGroup(t, "undo", 2, booting, results)

	asWantError(t, handleASXRollbackInstanceRefresh, url.Values{"AutoScalingGroupName": {"undo"}}, "ActiveInstanceRefreshNotFound")
	id := asStartRefresh(t, url.Values{
		"AutoScalingGroupName": {"undo"},
		"DesiredConfiguration.LaunchTemplate.LaunchTemplateName": {"new"},
		"DesiredConfiguration.LaunchTemplate.Version":            {"1"},
		"Preferences.MinHealthyPercentage":                       {"50"},
		"Preferences.InstanceWarmup":                             {"0"},
	})
	forward := <-booting
	if rec := asQuery(t, handleASXRollbackInstanceRefresh, url.Values{"AutoScalingGroupName": {"undo"}}); rec.Code != http.StatusOK {
		t.Fatalf("RollbackInstanceRefresh = %d %s", rec.Code, rec.Body)
	}
	ref := asWantRefresh(t, id, asRefreshRollbackInProgress, 50, 2)
	if ref.Rollback == nil || ref.Rollback.PercentageCompleteOnRollback != 0 || ref.Rollback.InstancesToUpdateOnRollback != 2 {
		t.Fatalf("rollback details = %+v", ref.Rollback)
	}
	asWantError(t, handleASXRollbackInstanceRefresh, url.Values{"AutoScalingGroupName": {"undo"}}, "ActiveInstanceRefreshNotFound")

	results <- nil
	back := <-booting
	if inst, _ := ec2Instances.Get(forward); inst.State != "terminated" {
		t.Fatalf("instance the refresh launched is %q during the rollback, want terminated", inst.State)
	}
	if inst, _ := ec2Instances.Get(back); inst.ImageId != "ami-launch" {
		t.Fatalf("rollback launched %s, want the launch configuration's ami-launch", inst.ImageId)
	}
	results <- nil
	w.await(id, asRefreshRollbackSuccessful)
	asWantRefresh(t, id, asRefreshRollbackSuccessful, 0, 2)
	asg, _ := autoScalingGroups.Get("undo")
	if asg.LaunchConfigurationName != "lc" || asg.LaunchTemplate.set() || len(asg.InstanceIds) != 2 {
		t.Fatalf("group after the rollback = %+v", asg)
	}
}

func TestAutoScalingInstanceRefreshWithoutDesiredConfigurationIsIrreversible(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	asRefreshGroup(t, "plain", 1, booting, results)

	asWantError(t, handleASXStartInstanceRefresh, url.Values{"AutoScalingGroupName": {"plain"}, "Preferences.AutoRollback": {"true"}}, "IrreversibleInstanceRefresh")
	id := asStartRefresh(t, url.Values{"AutoScalingGroupName": {"plain"}, "Preferences.InstanceWarmup": {"0"}})
	replacement := <-booting
	asWantError(t, handleASXRollbackInstanceRefresh, url.Values{"AutoScalingGroupName": {"plain"}}, "IrreversibleInstanceRefresh")
	results <- nil
	w.await(id, asRefreshSuccessful)
	asWantRefresh(t, id, asRefreshSuccessful, 100, 0)
	if inst, _ := ec2Instances.Get(replacement); inst.ImageId != "ami-launch" {
		t.Fatalf("replacement launched %s, want the group's own ami-launch", inst.ImageId)
	}
}

// TestAutoScalingInstanceRefreshCancelWaitsForTransitioningInstances shows a
// cancel holds the refresh Cancelling until the launch in flight ends, and
// keeps what the refresh already replaced.
func TestAutoScalingInstanceRefreshCancelWaitsForTransitioningInstances(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	asRefreshGroup(t, "stop", 2, booting, results)

	id := asStartRefresh(t, url.Values{
		"AutoScalingGroupName":             {"stop"},
		"Preferences.MinHealthyPercentage": {"50"},
		"Preferences.InstanceWarmup":       {"0"},
	})
	replacement := <-booting
	if rec := asQuery(t, handleASXCancelInstanceRefresh, url.Values{"AutoScalingGroupName": {"stop"}}); rec.Code != http.StatusOK {
		t.Fatalf("CancelInstanceRefresh = %d %s", rec.Code, rec.Body)
	}
	asWantRefresh(t, id, asRefreshCancelling, 0, 2)
	asWantError(t, handleASXCancelInstanceRefresh, url.Values{"AutoScalingGroupName": {"stop"}}, "ActiveInstanceRefreshNotFound")
	results <- nil
	w.await(id, asRefreshCancelled)
	ref := asWantRefresh(t, id, asRefreshCancelled, 0, 2)
	if ref.EndTime == "" {
		t.Fatal("cancelled refresh has no EndTime")
	}
	if got := asInstanceLifecycleState("stop", replacement); got != "InService" {
		t.Fatalf("replacement launched before the cancel is %s, want InService", got)
	}
	select {
	case id := <-booting:
		t.Fatalf("cancelled refresh launched %s", id)
	default:
	}
}

// TestAutoScalingInstanceRefreshFailsOnAFailedLaunch shows a replacement
// whose boot fails fails the refresh with the launch's reason, or, with auto
// rollback, rolls it back.
func TestAutoScalingInstanceRefreshFailsOnAFailedLaunch(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	asRefreshGroup(t, "fails", 1, booting, results)

	id := asStartRefresh(t, url.Values{"AutoScalingGroupName": {"fails"}, "Preferences.InstanceWarmup": {"0"}})
	<-booting
	results <- errors.New("guest never answered")
	w.await(id, asRefreshFailed)
	ref := asWantRefresh(t, id, asRefreshFailed, 0, 1)
	if !strings.Contains(ref.StatusReason, "guest never answered") {
		t.Fatalf("StatusReason = %q, want the launch failure", ref.StatusReason)
	}

	asRefreshGroup(t, "reverts", 2, booting, results)
	id = asStartRefresh(t, url.Values{
		"AutoScalingGroupName":                                 {"reverts"},
		"DesiredConfiguration.LaunchTemplate.LaunchTemplateId": {"lt-new"},
		"DesiredConfiguration.LaunchTemplate.Version":          {"1"},
		"Preferences.AutoRollback":                             {"true"},
		"Preferences.MinHealthyPercentage":                     {"50"},
		"Preferences.InstanceWarmup":                           {"0"},
	})
	forward := <-booting
	results <- nil
	<-booting
	results <- errors.New("guest never answered")
	back := <-booting
	if inst, _ := ec2Instances.Get(forward); inst.State != "terminated" {
		t.Fatalf("instance the refresh launched is %q during the auto rollback, want terminated", inst.State)
	}
	results <- nil
	w.await(id, asRefreshRollbackSuccessful)
	ref = asWantRefresh(t, id, asRefreshRollbackSuccessful, 0, 1)
	if ref.Rollback == nil || !strings.Contains(ref.Rollback.Reason, "guest never answered") {
		t.Fatalf("rollback details = %+v, want the launch failure that started the rollback", ref.Rollback)
	}
	if inst, _ := ec2Instances.Get(back); inst.ImageId != "ami-launch" {
		t.Fatalf("auto rollback launched %s, want the launch configuration's ami-launch", inst.ImageId)
	}
}

// TestAutoScalingInstanceRefreshSkipsMatchingInstances shows skip matching
// leaves the members already launched from the desired configuration, so a
// refresh with nothing to change succeeds without a launch.
func TestAutoScalingInstanceRefreshSkipsMatchingInstances(t *testing.T) {
	asLaunchTestStores(t)
	booting, results := asRefreshBoots(t)
	w := asWatchRefreshes(t)
	asRefreshGroup(t, "skip", 2, booting, results)

	asWantError(t, handleASXStartInstanceRefresh, url.Values{"AutoScalingGroupName": {"skip"}, "Preferences.SkipMatching": {"true"}}, "ValidationError")
	desired := url.Values{
		"AutoScalingGroupName":                                 {"skip"},
		"DesiredConfiguration.LaunchTemplate.LaunchTemplateId": {"lt-new"},
		"DesiredConfiguration.LaunchTemplate.Version":          {"1"},
		"Preferences.MinHealthyPercentage":                     {"0"},
		"Preferences.InstanceWarmup":                           {"0"},
		"Preferences.SkipMatching":                             {"true"},
	}
	first := asStartRefresh(t, desired)
	<-booting
	<-booting
	results <- nil
	results <- nil
	w.await(first, asRefreshSuccessful)
	asWantRefresh(t, first, asRefreshSuccessful, 100, 0)

	again := asStartRefresh(t, desired)
	w.await(again, asRefreshSuccessful)
	asWantRefresh(t, again, asRefreshSuccessful, 100, 0)
	select {
	case id := <-booting:
		t.Fatalf("skip matching replaced matching member with %s", id)
	default:
	}
}
