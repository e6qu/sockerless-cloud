package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	kmspb "cloud.google.com/go/kms/apiv1/kmspb"
	pspb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

// gcpContinuationList is one list method under test: put and del create and
// delete the item a name stands for, and page reads two items from token on.
type gcpContinuationList struct {
	put  func(t *testing.T, name string)
	del  func(t *testing.T, name string)
	page func(t *testing.T, token string) (names []string, next string)
}

// gcpCheckContinuation walks a list two items a page while it changes. order
// holds eight names in the list's own order, standing for a b c d e f h j. The
// list starts as b d f h j; once the first page has returned b and d, a client
// creates a and c ahead of the boundary and e after it, and deletes b, which it
// has read, and h, which it has not. A token that resumes after d lists e f j
// next, so the walk reads b d e f j. An index token lists d again and skips
// what the creations pushed past it.
func gcpCheckContinuation(t *testing.T, list gcpContinuationList, order [8]string) {
	t.Helper()
	a, b, c, d, e, f, h, j := order[0], order[1], order[2], order[3], order[4], order[5], order[6], order[7]
	for _, name := range []string{b, d, f, h, j} {
		list.put(t, name)
	}
	seen, next := list.page(t, "")
	if strings.Join(seen, " ") != b+" "+d || next == "" {
		t.Fatalf("first page listed %v with token %q, want [%s %s] and a token", seen, next, b, d)
	}
	list.put(t, a)
	list.put(t, c)
	list.put(t, e)
	list.del(t, b)
	list.del(t, h)
	for pages := 1; next != ""; pages++ {
		if pages > 8 {
			t.Fatalf("the list never reached its last page: %v", seen)
		}
		var names []string
		names, next = list.page(t, next)
		seen = append(seen, names...)
	}
	if want := []string{b, d, e, f, j}; strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Fatalf("walking the list while it changed read %v, want %v", seen, want)
	}
}

var gcpContinuationLetters = [8]string{"a", "b", "c", "d", "e", "f", "h", "j"}

func TestCloudKMSKeyRingsResumeAfterTheLastKeyRingReturned(t *testing.T) {
	saved := kmsKeyRings
	t.Cleanup(func() { kmsKeyRings = saved })
	kmsKeyRings = sim.MakeStore[kmsKeyRing](nil, "test_kms_key_rings")
	const parent = "projects/p/locations/global"
	s := &cloudKmsGRPC{}
	gcpCheckContinuation(t, gcpContinuationList{
		put: func(_ *testing.T, id string) {
			kmsKeyRings.Put(parent+"/keyRings/"+id, kmsKeyRing{Name: parent + "/keyRings/" + id})
		},
		del: func(_ *testing.T, id string) { kmsKeyRings.Delete(parent + "/keyRings/" + id) },
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := s.ListKeyRings(context.Background(), &kmspb.ListKeyRingsRequest{Parent: parent, PageSize: 2, PageToken: token})
			if err != nil {
				t.Fatalf("ListKeyRings(%q): %v", token, err)
			}
			var ids []string
			for _, ring := range resp.GetKeyRings() {
				ids = append(ids, strings.TrimPrefix(ring.GetName(), parent+"/keyRings/"))
			}
			return ids, resp.GetNextPageToken()
		},
	}, gcpContinuationLetters)
}

// Cloud KMS lists a key's versions in version number order, so 10 follows 9.
func TestCloudKMSCryptoKeyVersionsResumeInVersionNumberOrder(t *testing.T) {
	savedKeys, savedVersions := kmsCryptoKeys, kmsCryptoKeyVersions
	t.Cleanup(func() { kmsCryptoKeys, kmsCryptoKeyVersions = savedKeys, savedVersions })
	kmsCryptoKeys = sim.MakeStore[kmsStoredCryptoKey](nil, "test_kms_crypto_keys")
	kmsCryptoKeyVersions = sim.MakeStore[kmsCryptoKeyVersion](nil, "test_kms_crypto_key_versions")
	const key = "projects/p/locations/global/keyRings/r/cryptoKeys/k"
	kmsCryptoKeys.Put(key, kmsStoredCryptoKey{Name: key, Purpose: "ENCRYPT_DECRYPT"})
	s := &cloudKmsGRPC{}
	gcpCheckContinuation(t, gcpContinuationList{
		put: func(_ *testing.T, n string) {
			kmsCryptoKeyVersions.Put(key+"/cryptoKeyVersions/"+n, kmsCryptoKeyVersion{Name: key + "/cryptoKeyVersions/" + n, State: "ENABLED"})
		},
		del: func(_ *testing.T, n string) { kmsCryptoKeyVersions.Delete(key + "/cryptoKeyVersions/" + n) },
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := s.ListCryptoKeyVersions(context.Background(), &kmspb.ListCryptoKeyVersionsRequest{Parent: key, PageSize: 2, PageToken: token})
			if err != nil {
				t.Fatalf("ListCryptoKeyVersions(%q): %v", token, err)
			}
			var numbers []string
			for _, version := range resp.GetCryptoKeyVersions() {
				numbers = append(numbers, strings.TrimPrefix(version.GetName(), key+"/cryptoKeyVersions/"))
			}
			return numbers, resp.GetNextPageToken()
		},
	}, [8]string{"8", "9", "10", "11", "12", "13", "20", "100"})
}

func TestPubSubTopicsResumeAfterTheLastTopicReturned(t *testing.T) {
	saved := psTopics
	t.Cleanup(func() { psTopics = saved })
	psTopics = sim.MakeStore[PSTopic](nil, "test_pubsub_topics")
	const prefix = "projects/p/topics/"
	s := &pubsubPublisherGRPC{}
	gcpCheckContinuation(t, gcpContinuationList{
		put: func(_ *testing.T, id string) { psTopics.Put(prefix+id, PSTopic{Name: prefix + id}) },
		del: func(_ *testing.T, id string) { psTopics.Delete(prefix + id) },
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := s.ListTopics(context.Background(), &pspb.ListTopicsRequest{Project: "projects/p", PageSize: 2, PageToken: token})
			if err != nil {
				t.Fatalf("ListTopics(%q): %v", token, err)
			}
			var ids []string
			for _, topic := range resp.GetTopics() {
				ids = append(ids, strings.TrimPrefix(topic.GetName(), prefix))
			}
			return ids, resp.GetNextPageToken()
		},
	}, gcpContinuationLetters)
}

// gcpContinuationGet reads one page of a REST list through the simulator as
// host and decodes it into out.
func gcpContinuationGet(t *testing.T, srv *sim.Server, host, path string, out any) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s answered %d: %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, rec.Body)
	}
}

// The Cloud Run Admin API v1 pages a Knative list by its continue token.
func TestCloudRunV1JobsResumeAfterTheLastJobReturned(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	prefix := cloudRunV1JobsPrefix(gcpTestProjectID)
	gcpCheckContinuation(t, gcpContinuationList{
		put: func(_ *testing.T, id string) { crjJobs.Put(prefix+id, Job{Name: prefix + id}) },
		del: func(_ *testing.T, id string) { crjJobs.Delete(prefix + id) },
		page: func(t *testing.T, token string) ([]string, string) {
			query := url.Values{"limit": {"2"}}
			if token != "" {
				query.Set("continue", token)
			}
			var list CRJobList
			gcpContinuationGet(t, srv, "run.googleapis.com",
				"/apis/run.googleapis.com/v1/namespaces/"+gcpTestProjectID+"/jobs?"+query.Encode(), &list)
			var ids []string
			for _, job := range list.Items {
				ids = append(ids, job.Metadata.Name)
			}
			next := ""
			if list.Metadata != nil {
				next = list.Metadata.Continue
			}
			return ids, next
		},
	}, gcpContinuationLetters)
}

// roles.list pages a project's custom roles in name order.
func TestIAMRolesListResumesAfterTheLastRoleReturned(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	prefix := "projects/" + gcpTestProjectID + "/roles/"
	gcpCheckContinuation(t, gcpContinuationList{
		put: func(_ *testing.T, id string) {
			iamCustomRoles.Put(prefix+id, GCPCustomRole{Name: prefix + id, Title: id, Stage: "GA", Etag: "BwX="})
		},
		del: func(_ *testing.T, id string) { iamCustomRoles.Delete(prefix + id) },
		page: func(t *testing.T, token string) ([]string, string) {
			query := url.Values{"pageSize": {"2"}}
			if token != "" {
				query.Set("pageToken", token)
			}
			var resp struct {
				Roles []struct {
					Name string `json:"name"`
				} `json:"roles"`
				NextPageToken string `json:"nextPageToken"`
			}
			gcpContinuationGet(t, srv, "iam.googleapis.com", "/v1/projects/"+gcpTestProjectID+"/roles?"+query.Encode(), &resp)
			var ids []string
			for _, role := range resp.Roles {
				ids = append(ids, strings.TrimPrefix(role.Name, prefix))
			}
			return ids, resp.NextPageToken
		},
	}, gcpContinuationLetters)
}

// The predefined roles.list honours its documented default page size of 300.
func TestIAMPredefinedRolesListPages(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	var all []string
	token := ""
	for pages := 0; ; pages++ {
		if pages > len(gcpPredefinedRoles()) {
			t.Fatal("roles.list never reached its last page")
		}
		query := url.Values{"pageSize": {"1"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		var resp struct {
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
			NextPageToken string `json:"nextPageToken"`
		}
		gcpContinuationGet(t, srv, "iam.googleapis.com", "/v1/roles?"+query.Encode(), &resp)
		if len(resp.Roles) != 1 {
			t.Fatalf("pageSize=1 listed %d roles", len(resp.Roles))
		}
		all = append(all, resp.Roles[0].Name)
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	if len(all) != len(gcpPredefinedRoles()) {
		t.Fatalf("paging read %d roles, the catalog holds %d", len(all), len(gcpPredefinedRoles()))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("roles.list is not in name order: %s before %s", all[i-1], all[i])
		}
	}
}

// entries.list orders entries by timestamp, and identical timestamps by
// insertId, in either direction; a token resumes after the last entry
// returned, whatever was written in between.
func TestCloudLoggingEntriesResumeAfterTheLastEntryReturned(t *testing.T) {
	for _, tc := range []struct {
		orderBy string
		order   [8]string
	}{
		{"timestamp asc", [8]string{"01", "02", "03", "04", "05", "06", "08", "10"}},
		{"timestamp desc", [8]string{"10", "08", "06", "05", "04", "03", "02", "01"}},
	} {
		t.Run(tc.orderBy, func(t *testing.T) {
			useLoggingMemoryStores(t)
			const logName = "projects/p/logs/continuation"
			minute := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
			gcpCheckContinuation(t, gcpContinuationList{
				put: func(t *testing.T, second string) {
					s, _ := strconv.Atoi(second)
					if err := writeLogEntries(logName, nil, nil, []LogEntry{{
						InsertID:  second,
						Timestamp: minute.Add(time.Duration(s) * time.Second).Format(time.RFC3339),
					}}); err != nil {
						t.Fatal(err)
					}
				},
				del: func(_ *testing.T, second string) {
					logEntries.Edit(logName, func(e *logstore.Entry[loggingStoredEntry]) (bool, bool) {
						return e.Value.Entry.InsertID != second, false
					})
				},
				page: func(t *testing.T, token string) ([]string, string) {
					entries, next, err := listLogEntries("", []string{"projects/p"}, 2, token, tc.orderBy)
					if err != nil {
						t.Fatalf("listLogEntries(%q): %v", token, err)
					}
					var ids []string
					for _, entry := range entries {
						ids = append(ids, entry.InsertID)
					}
					return ids, next
				},
			}, tc.order)
		})
	}
}
