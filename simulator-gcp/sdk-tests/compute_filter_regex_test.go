package gcp_sdk_test

import (
	"errors"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

// compute.networks.insert refuses a network without the name the Discovery
// document requires, with the field error whose reason a client branches on.
func TestCompute_NetworkInsertRequiresAName(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Networks.Insert("test-project", &compute.Network{AutoCreateSubnetworks: true}).Do()
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, http.StatusBadRequest, gerr.Code)
	assert.Equal(t, "Required field 'resource.name' not specified", gerr.Message)
	require.Len(t, gerr.Errors, 1)
	assert.Equal(t, "required", gerr.Errors[0].Reason)

	// Every other insert answers the same field errors.
	_, err = svc.Firewalls.Insert("test-project", &compute.Firewall{Network: "global/networks/default"}).Do()
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	require.Len(t, gerr.Errors, 1)
	assert.Equal(t, "required", gerr.Errors[0].Reason)
	_, err = svc.HealthChecks.Insert("test-project", &compute.HealthCheck{Name: "Bad_Name", Type: "TCP",
		TcpHealthCheck: &compute.TCPHealthCheck{Port: 80}}).Do()
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, http.StatusBadRequest, gerr.Code)
	require.Len(t, gerr.Errors, 1)
	assert.Equal(t, "invalid", gerr.Errors[0].Reason)
	assert.Contains(t, gerr.Message, "Invalid value for field 'resource.name': 'Bad_Name'")
}

// A Compute Engine list filter written as `field eq|ne literal` matches the
// literal as a regular expression against the whole field.
func TestCompute_ListFilterRegexForm(t *testing.T) {
	svc := computeService(t)
	const project = "regex-filter-project"
	for _, name := range []string{"web-check", "web-check-2", "db-check"} {
		_, err := svc.HealthChecks.Insert(project, &compute.HealthCheck{Name: name, Type: "TCP",
			TcpHealthCheck: &compute.TCPHealthCheck{Port: 80}}).Do()
		require.NoError(t, err)
	}
	names := func(filter string) []string {
		t.Helper()
		list, err := svc.HealthChecks.List(project).Filter(filter).Do()
		require.NoError(t, err)
		out := []string{}
		for _, hc := range list.Items {
			out = append(out, hc.Name)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, []string{"web-check", "web-check-2"}, names("name eq web-.*"))
	assert.Equal(t, []string{"db-check"}, names("name ne web-.*"))
	assert.Equal(t, []string{"web-check"}, names(`(name eq "web-.*") (name ne ".*-2")`))
	assert.Equal(t, []string{"db-check"}, names(`name = "db-check"`), "an AIP-160 filter keeps its meaning")

	_, err := svc.HealthChecks.List(project).Filter(`(name eq web-.*) (type = "TCP")`).Do()
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "a filter mixing both languages must be refused, got %v", err)
	assert.Equal(t, http.StatusBadRequest, gerr.Code)
	assert.Contains(t, gerr.Message, "cannot be combined")
}
