package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Queues data plane — the queue's stored access policies and the
// service-level configuration and statistics.
//
// Wire reference: https://learn.microsoft.com/rest/api/storageservices/queue-service-rest-api

// QueueServiceConfig is an account's Queue service properties document as Set
// Service Properties stored it. Get Service Properties reads back exactly what
// was written.
type QueueServiceConfig struct {
	Account    string
	Properties queueServiceProperties
	Configured bool
}

var queueServiceConfigs sim.Store[QueueServiceConfig]

func registerQueuesDataPlaneStores(srv *sim.Server) {
	queueServiceConfigs = sim.MakeStore[QueueServiceConfig](srv.DB(), "queue_service_properties")
}

// queueServiceProperties is the Queue service's Set/Get Service Properties
// document. Unlike the File service, the Queue service carries Storage
// Analytics logging.
type queueServiceProperties struct {
	XMLName       xml.Name                `xml:"StorageServiceProperties"`
	Logging       *queueAnalyticsLogging  `xml:"Logging,omitempty"`
	HourMetrics   *storageAnalyticsMetric `xml:"HourMetrics,omitempty"`
	MinuteMetrics *storageAnalyticsMetric `xml:"MinuteMetrics,omitempty"`
	Cors          []storageCorsRule       `xml:"Cors>CorsRule,omitempty"`
}

type queueAnalyticsLogging struct {
	Version         string                     `xml:"Version"`
	Delete          bool                       `xml:"Delete"`
	Read            bool                       `xml:"Read"`
	Write           bool                       `xml:"Write"`
	RetentionPolicy *storageAnalyticsRetention `xml:"RetentionPolicy,omitempty"`
}

// defaultQueueServiceProperties is the document an account's Queue service
// carries before anything has been written to it.
func defaultQueueServiceProperties() queueServiceProperties {
	return queueServiceProperties{
		Logging: &queueAnalyticsLogging{
			Version:         "1.0",
			RetentionPolicy: &storageAnalyticsRetention{},
		},
		HourMetrics: &storageAnalyticsMetric{
			Version:         "1.0",
			RetentionPolicy: &storageAnalyticsRetention{},
		},
		MinuteMetrics: &storageAnalyticsMetric{
			Version:         "1.0",
			RetentionPolicy: &storageAnalyticsRetention{},
		},
	}
}

func handleQueuesGetServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	props := defaultQueueServiceProperties()
	if stored, ok := queueServiceConfigs.Get(account); ok && stored.Configured {
		props = stored.Properties
	}
	writeStorageXML(w, http.StatusOK, props)
}

// handleQueuesSetServiceProperties is Set Queue Service Properties: the
// document the request carries becomes the account's Queue service
// configuration, and Get Service Properties reads back exactly what was
// written.
func handleQueuesSetServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	var props queueServiceProperties
	if err := xml.Unmarshal(body, &props); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"The XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	queueServiceConfigs.Put(account, QueueServiceConfig{
		Account: account, Properties: props, Configured: true,
	})
	w.WriteHeader(http.StatusAccepted)
}

// handleQueuesGetServiceStatistics is Get Queue Service Statistics. The
// simulator runs one replica of an account's queues, so the replica a read
// reaches is the one every write has already been applied to and the last sync
// time is now.
func handleQueuesGetServiceStatistics(w http.ResponseWriter, r *http.Request, account string) {
	type geoReplication struct {
		Status       string `xml:"Status"`
		LastSyncTime string `xml:"LastSyncTime"`
	}
	type serviceStats struct {
		XMLName        xml.Name       `xml:"StorageServiceStats"`
		GeoReplication geoReplication `xml:"GeoReplication"`
	}
	writeStorageXML(w, http.StatusOK, serviceStats{
		GeoReplication: geoReplication{
			Status:       "live",
			LastSyncTime: time.Now().UTC().Format(http.TimeFormat),
		},
	})
}

// handleQueueACL is Get / Set Queue ACL: the stored access policies a shared
// access signature can be issued against.
func handleQueueACL(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	data, ok := queueData.Get(key)
	if !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeStorageXML(w, http.StatusOK, TableSignedIdentifiers{Items: data.ACLs})
	case http.MethodPut:
		defer r.Body.Close()
		var body TableSignedIdentifiers
		if err := xml.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeStorageError(w, "InvalidXmlDocument",
				"The XML specified is not syntactically valid.", http.StatusBadRequest)
			return
		}
		if len(body.Items) > 5 {
			writeStorageError(w, "InvalidXmlDocument",
				"A queue can contain at most five stored access policies.", http.StatusBadRequest)
			return
		}
		queueData.Update(key, func(q *QueueData) {
			q.ACLs = body.Items
		})
		w.WriteHeader(http.StatusNoContent)
	default:
		writeStorageOperationNotImplemented(w, r, "Queues")
	}
}
