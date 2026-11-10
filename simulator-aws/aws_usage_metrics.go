package main

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch publishes AWS usage metrics in the AWS/Usage namespace: CallCount
// for the API operations of the services the "AWS API usage metrics" page
// lists, and ResourceCount for the resources Amazon EC2, AWS Fargate, Amazon
// ECR and Amazon Data Firehose count against their quotas, each as its own
// usage metrics page describes.

const awsUsageNamespace = "AWS/Usage"

// awsUsageServiceBySource is the Service dimension value the "AWS API usage
// metrics" table gives each service the simulator serves.
var awsUsageServiceBySource = map[string]string{
	"acm.amazonaws.com":                     "Certificate Manager",
	"acm-pca.amazonaws.com":                 "Private Certificate Authority",
	"apigateway.amazonaws.com":              "API Gateway",
	"application-autoscaling.amazonaws.com": "Application Auto Scaling",
	"autoscaling.amazonaws.com":             "EC2 Auto Scaling",
	"batch.amazonaws.com":                   "Batch",
	"budgets.amazonaws.com":                 "Budgets",
	"servicediscovery.amazonaws.com":        "Cloud Map",
	"cloudtrail.amazonaws.com":              "CloudTrail",
	"monitoring.amazonaws.com":              "CloudWatch",
	"logs.amazonaws.com":                    "Logs",
	"codebuild.amazonaws.com":               "CodeBuild",
	"ec2.amazonaws.com":                     "EC2",
	"ecr.amazonaws.com":                     "ECR",
	"ecs.amazonaws.com":                     "ECS",
	"elasticfilesystem.amazonaws.com":       "EFS",
	"elasticloadbalancing.amazonaws.com":    "Elastic Load Balancing",
	"firehose.amazonaws.com":                "Firehose",
	"kms.amazonaws.com":                     "KMS",
	"lambda.amazonaws.com":                  "Lambda",
	"organizations.amazonaws.com":           "Organizations",
	"pi.amazonaws.com":                      "Performance Insights",
	"rds.amazonaws.com":                     "RDS",
	"s3.amazonaws.com":                      "S3",
	"secretsmanager.amazonaws.com":          "Secrets Manager",
	"servicequotas.amazonaws.com":           "Service Quotas",
	"sns.amazonaws.com":                     "SNS",
	"sqs.amazonaws.com":                     "SQS",
	"wafv2.amazonaws.com":                   "WAF",
}

// awsUsageTrackedOperations narrows a service to the operations its own
// usage metrics page lists; a service absent here reports every operation.
var awsUsageTrackedOperations = map[string]map[string]bool{
	"CloudWatch": setOf("DeleteAlarms", "DeleteDashboards", "DescribeAlarmHistory", "DescribeAlarms", "GetDashboard",
		"GetMetricData", "GetMetricStatistics", "ListMetrics", "PutDashboard", "PutMetricData"),
	"ECR": setOf("GetAuthorizationToken", "BatchCheckLayerAvailability", "InitiateLayerUpload", "UploadLayerPart",
		"CompleteLayerUpload", "PutImage", "BatchGetImage", "GetDownloadUrlForLayer"),
}

func setOf(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

var awsUsageThrottleCodes = setOf("ThrottlingException", "Throttling", "TooManyRequestsException",
	"RequestLimitExceeded", "LimitExceededException", "RequestThrottled", "ThrottledException")

// awsUsageAPIDimensions are a CallCount's dimensions: Amazon ECR uses no
// Class dimension.
func awsUsageAPIDimensions(service, operation string) []CWDimension {
	dims := []CWDimension{{Name: "Resource", Value: operation}, {Name: "Service", Value: service}, {Name: "Type", Value: "API"}}
	if service != "ECR" {
		dims = append([]CWDimension{{Name: "Class", Value: "None"}}, dims...)
	}
	return dims
}

// awsUsageRecordCall counts one API call a client signed. CloudWatch Logs also
// publishes CallCount, ErrorCount and ThrottleCount in AWS/Logs.
func awsUsageRecordCall(source, operation, errorCode string, signed bool) {
	if cwMetrics == nil || !signed || operation == "" {
		return
	}
	service, ok := awsUsageServiceBySource[source]
	if !ok {
		return
	}
	if counter, ok := awsUsageResourceCounters[service]; ok {
		counter.publish(false)
	}
	if tracked, narrowed := awsUsageTrackedOperations[service]; narrowed && !tracked[operation] {
		return
	}
	now := float64(time.Now().Unix())
	dims := awsUsageAPIDimensions(service, operation)
	cwStoreDatum(CWMetricDatum{Namespace: awsUsageNamespace, MetricName: "CallCount", Dimensions: dims, Value: 1, Timestamp: now, Unit: "None"})
	if service == "Logs" {
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "CallCount", Dimensions: dims, Value: 1, Timestamp: now, Unit: "None"})
		if errorCode != "" {
			cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "ErrorCount", Dimensions: dims, Value: 1, Timestamp: now, Unit: "None"})
		}
		if awsUsageThrottleCodes[errorCode] {
			cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "ThrottleCount", Dimensions: dims, Value: 1, Timestamp: now, Unit: "None"})
		}
	}
}

// awsUsageCounter publishes one service's ResourceCount metrics: at its
// collection cadence, and as soon as a call to the service changes a count.
type awsUsageCounter struct {
	every time.Duration
	count func() map[string]awsUsageCount

	mu   sync.Mutex
	last map[string]float64
}

type awsUsageCount struct {
	dims  []CWDimension
	value float64
}

func (c *awsUsageCounter) publish(always bool) {
	counts := c.count()
	if counts == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]float64{}
	}
	for k := range c.last {
		if _, still := counts[k]; !still {
			counts[k] = awsUsageCount{dims: awsUsageParseKey(k)}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := float64(time.Now().Unix())
	for _, k := range keys {
		count := counts[k]
		if previous, seen := c.last[k]; seen && previous == count.value && !always {
			continue
		}
		c.last[k] = count.value
		cwStoreDatum(CWMetricDatum{Namespace: awsUsageNamespace, MetricName: "ResourceCount", Dimensions: count.dims, Value: count.value, Timestamp: now, Unit: "None"})
	}
}

func awsUsageKey(dims []CWDimension) string {
	parts := make([]string, 0, len(dims))
	for _, d := range dims {
		parts = append(parts, d.Name+"="+d.Value)
	}
	return strings.Join(parts, "\x00")
}

func awsUsageParseKey(key string) []CWDimension {
	var dims []CWDimension
	for _, part := range strings.Split(key, "\x00") {
		name, value, _ := strings.Cut(part, "=")
		dims = append(dims, CWDimension{Name: name, Value: value})
	}
	return dims
}

func awsUsageCounts(entries ...awsUsageCount) map[string]awsUsageCount {
	out := map[string]awsUsageCount{}
	for _, e := range entries {
		key := awsUsageKey(e.dims)
		prev := out[key]
		out[key] = awsUsageCount{dims: e.dims, value: prev.value + e.value}
	}
	return out
}

// awsUsageEC2Class names the Class an On-Demand instance type counts under:
// the vCPU quota classes the Amazon EC2 usage metrics page lists, by the type's
// leading letters.
func awsUsageEC2Class(instanceType string) (string, bool) {
	family := strings.ToLower(instanceType)
	switch {
	case strings.HasPrefix(family, "inf"):
		return "Inf/OnDemand", true
	case strings.HasPrefix(family, "f"):
		return "F/OnDemand", true
	case strings.HasPrefix(family, "g"):
		return "G/OnDemand", true
	case strings.HasPrefix(family, "p"):
		return "P/OnDemand", true
	case strings.HasPrefix(family, "x"):
		return "X/OnDemand", true
	case family != "" && strings.ContainsRune("acdhimrtz", rune(family[0])):
		return "Standard/OnDemand", true
	}
	return "", false
}

var awsUsageResourceCounters = map[string]*awsUsageCounter{
	"EC2": {every: time.Minute, count: func() map[string]awsUsageCount {
		if ec2Instances == nil {
			return nil
		}
		var entries []awsUsageCount
		for _, class := range []string{"Standard/OnDemand", "F/OnDemand", "G/OnDemand", "Inf/OnDemand", "P/OnDemand", "X/OnDemand"} {
			entries = append(entries, awsUsageCount{dims: awsUsageResourceDims("EC2", "vCPU", class)})
		}
		for _, inst := range ec2Instances.List() {
			if inst.State != "running" || inst.InstanceLifecycle == "spot" {
				continue
			}
			class, ok := awsUsageEC2Class(inst.InstanceType)
			facts, known := ec2LookupInstanceType(inst.InstanceType)
			if !ok || !known {
				continue
			}
			entries = append(entries, awsUsageCount{dims: awsUsageResourceDims("EC2", "vCPU", class), value: float64(facts.VCpus)})
		}
		return awsUsageCounts(entries...)
	}},
	"ECS": {every: time.Minute, count: func() map[string]awsUsageCount {
		if ecsTasks == nil {
			return nil
		}
		entries := []awsUsageCount{{dims: awsUsageResourceDims("Fargate", "vCPU", "Standard/OnDemand")}}
		for _, task := range ecsTasks.List() {
			if task.LaunchType != "FARGATE" || task.LastStatus == ECSTaskStatusStopped {
				continue
			}
			units, err := strconv.ParseFloat(task.Cpu, 64)
			if err != nil {
				continue
			}
			entries = append(entries, awsUsageCount{dims: awsUsageResourceDims("Fargate", "vCPU", "Standard/OnDemand"), value: units / 1024})
		}
		return awsUsageCounts(entries...)
	}},
	"ECR": {every: 5 * time.Minute, count: func() map[string]awsUsageCount {
		if ecrRepositories == nil || ecrImages == nil {
			return nil
		}
		repos := ecrRepositories.List()
		entries := []awsUsageCount{{dims: awsUsageECRDims("RepositoryCount", ""), value: float64(len(repos))}}
		digests := map[string]map[string]bool{}
		for _, img := range ecrImages.List() {
			if digests[img.RepositoryName] == nil {
				digests[img.RepositoryName] = map[string]bool{}
			}
			digests[img.RepositoryName][img.ImageDigest] = true
		}
		for _, repo := range repos {
			entries = append(entries, awsUsageCount{dims: awsUsageECRDims("ImagesPerRepositoryCount", repo.RepositoryName),
				value: float64(len(digests[repo.RepositoryName]))})
		}
		return awsUsageCounts(entries...)
	}},
	"Firehose": {every: 3 * time.Minute, count: func() map[string]awsUsageCount {
		if firehoseStreams == nil {
			return nil
		}
		return awsUsageCounts(awsUsageCount{dims: awsUsageResourceDims("Firehose", "DeliveryStreams", "None"), value: float64(firehoseStreams.Len())})
	}},
}

func awsUsageResourceDims(service, resource, class string) []CWDimension {
	return []CWDimension{{Name: "Class", Value: class}, {Name: "Resource", Value: resource}, {Name: "Service", Value: service}, {Name: "Type", Value: "Resource"}}
}

func awsUsageECRDims(resource, repository string) []CWDimension {
	dims := []CWDimension{{Name: "Resource", Value: resource}}
	if repository != "" {
		dims = append(dims, CWDimension{Name: "ResourceId", Value: "repository/" + repository})
	}
	return append(dims, CWDimension{Name: "Service", Value: "ECR"}, CWDimension{Name: "Type", Value: "RESOURCE"})
}

// startAWSUsageMetrics publishes every service's ResourceCount at its
// cadence.
func startAWSUsageMetrics(srv *sim.Server) {
	for name, counter := range awsUsageResourceCounters {
		srv.StartBackground("aws-usage-"+strings.ToLower(name), func(ctx context.Context) {
			counter.publish(true)
			ticker := time.NewTicker(counter.every)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					counter.publish(true)
				}
			}
		})
	}
}

// awsUsageMetricRef is a quota's UsageMetric.
type awsUsageMetricRef struct {
	MetricName string
	Dimensions []CWDimension
	Statistic  string
}

var (
	awsUsageRatePattern     = regexp.MustCompile(`^Rate of ([A-Za-z]+) requests$`)
	awsUsageLogsRatePattern = regexp.MustCompile(`^([A-Za-z]+) throttle limit in transactions per second$`)
)

// sqUsageMetric answers the usage metric a quota's usage is measured by: the
// resource counts the usage metrics pages tie to a quota, and the CallCount of
// the operation a request-rate quota is named for.
func sqUsageMetric(q sqCatalogQuota) (awsUsageMetricRef, bool) {
	resource := func(dims []CWDimension) (awsUsageMetricRef, bool) {
		return awsUsageMetricRef{MetricName: "ResourceCount", Dimensions: dims, Statistic: "Maximum"}, true
	}
	switch q.ServiceCode + "/" + q.QuotaCode {
	case "ec2/L-1216C47A":
		return resource(awsUsageResourceDims("EC2", "vCPU", "Standard/OnDemand"))
	case "ec2/L-74FC7D96":
		return resource(awsUsageResourceDims("EC2", "vCPU", "F/OnDemand"))
	case "ec2/L-DB2E81BA":
		return resource(awsUsageResourceDims("EC2", "vCPU", "G/OnDemand"))
	case "ec2/L-1945791B":
		return resource(awsUsageResourceDims("EC2", "vCPU", "Inf/OnDemand"))
	case "ec2/L-417A185B":
		return resource(awsUsageResourceDims("EC2", "vCPU", "P/OnDemand"))
	case "ec2/L-7295265B":
		return resource(awsUsageResourceDims("EC2", "vCPU", "X/OnDemand"))
	case "fargate/L-3032A538":
		return resource(awsUsageResourceDims("Fargate", "vCPU", "Standard/OnDemand"))
	case "fargate/L-36FBB829":
		return resource(awsUsageResourceDims("Fargate", "vCPU", "Standard/Spot"))
	case "ecr/L-CFEB8E8D":
		return resource(awsUsageECRDims("RepositoryCount", ""))
	case "ecr/L-03A36CE1":
		return resource(awsUsageECRDims("ImagesPerRepositoryCount", ""))
	case "firehose/L-14BB0BE7":
		return resource(awsUsageResourceDims("Firehose", "DeliveryStreams", "None"))
	}
	var service, operation string
	switch q.ServiceCode {
	case "monitoring", "ecr":
		service = map[string]string{"monitoring": "CloudWatch", "ecr": "ECR"}[q.ServiceCode]
		if m := awsUsageRatePattern.FindStringSubmatch(q.QuotaName); m != nil {
			operation = m[1]
		}
	case "logs":
		service = "Logs"
		if m := awsUsageLogsRatePattern.FindStringSubmatch(q.QuotaName); m != nil {
			operation = m[1]
		}
	}
	if operation == "" {
		return awsUsageMetricRef{}, false
	}
	if tracked, narrowed := awsUsageTrackedOperations[service]; narrowed && !tracked[operation] {
		return awsUsageMetricRef{}, false
	}
	return awsUsageMetricRef{MetricName: "CallCount", Dimensions: awsUsageAPIDimensions(service, operation), Statistic: "Sum"}, true
}

// awsUsageMatches reports whether a metric is the usage metric m names, a
// metric that adds a ResourceId to it included.
func awsUsageMatches(m awsUsageMetricRef, namespace, metricName string, dims []CWDimension) bool {
	if namespace != awsUsageNamespace || metricName != m.MetricName {
		return false
	}
	have := map[string]string{}
	for _, d := range dims {
		if d.Name != "ResourceId" {
			have[d.Name] = d.Value
		}
	}
	if len(have) != len(m.Dimensions) {
		return false
	}
	for _, d := range m.Dimensions {
		if have[d.Name] != d.Value {
			return false
		}
	}
	return true
}

// sqQuotaForUsageMetric answers the quota a usage metric measures.
func sqQuotaForUsageMetric(ref CWMetricRef) (sqCatalogQuota, bool) {
	for _, q := range sqCatalog {
		if m, ok := sqUsageMetric(q); ok && awsUsageMatches(m, ref.Namespace, ref.MetricName, ref.Dimensions) {
			return q, true
		}
	}
	return sqCatalogQuota{}, false
}

// awsUsageObserved answers a quota's highest usage in [start, end]: the most
// any one-minute period of its usage metric reached by the recommended
// statistic, per second for a rate quota.
func awsUsageObserved(m awsUsageMetricRef, perSecond bool, start, end time.Time) float64 {
	high := 0.0
	for _, series := range cwMetrics.List() {
		if len(series) == 0 || !awsUsageMatches(m, series[0].Namespace, series[0].MetricName, series[0].Dimensions) {
			continue
		}
		buckets := map[int64][]float64{}
		for _, d := range series {
			ts := int64(d.Timestamp)
			if ts < start.Unix() || ts > end.Unix() {
				continue
			}
			buckets[ts/60] = append(buckets[ts/60], d.Value)
		}
		for _, vals := range buckets {
			v := cwApplyStat(m.Statistic, vals)
			if perSecond {
				v /= 60
			}
			high = max(high, v)
		}
	}
	return high
}
