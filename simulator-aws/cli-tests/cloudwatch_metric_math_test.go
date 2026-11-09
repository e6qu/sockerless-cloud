package aws_cli_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putCLIMinutes records values[i] at minute i after t0; a nil leaves the
// minute without a datapoint.
func putCLIMinutes(t *testing.T, ns, metric string, dims []map[string]string, t0 time.Time, values ...*float64) {
	t.Helper()
	var data []map[string]any
	for i, v := range values {
		if v == nil {
			continue
		}
		d := map[string]any{"MetricName": metric, "Value": *v, "Timestamp": t0.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		if len(dims) > 0 {
			d["Dimensions"] = dims
		}
		data = append(data, d)
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns, "--metric-data", string(raw)))
}

func num(v float64) *float64 { return &v }

type cliMetricDataResult struct {
	Id         string    `json:"Id"`
	Label      string    `json:"Label"`
	StatusCode string    `json:"StatusCode"`
	Timestamps []string  `json:"Timestamps"`
	Values     []float64 `json:"Values"`
}

func cliGetMetricData(t *testing.T, queries []map[string]any, start, end time.Time) map[string][]cliMetricDataResult {
	t.Helper()
	raw, err := json.Marshal(queries)
	require.NoError(t, err)
	out := runCLI(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", string(raw),
		"--start-time", start.Format(time.RFC3339), "--end-time", end.Format(time.RFC3339), "--scan-by", "TimestampAscending", "--output", "json"))
	var resp struct {
		MetricDataResults []cliMetricDataResult `json:"MetricDataResults"`
	}
	parseJSON(t, out, &resp)
	byID := map[string][]cliMetricDataResult{}
	for _, r := range resp.MetricDataResults {
		byID[r.Id] = append(byID[r.Id], r)
	}
	return byID
}

func cliMetric(id, ns, metric string) map[string]any {
	return map[string]any{"Id": id, "ReturnData": false, "MetricStat": map[string]any{
		"Metric": map[string]any{"Namespace": ns, "MetricName": metric}, "Period": 60, "Stat": "Average",
	}}
}

// TestCloudWatchCLI_MetricMathExpressions runs IF, FILL, SEARCH and a Metrics
// Insights query through get-metric-data.
func TestCloudWatchCLI_MetricMathExpressions(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathCLI")
	t0 := time.Now().UTC().Truncate(time.Minute).Add(-15 * time.Minute)
	putCLIMinutes(t, ns, "One", nil, t0, num(1), num(1), num(0), num(0), nil)
	putCLIMinutes(t, ns, "Two", nil, t0, num(30), nil, num(0), num(0), num(30))
	putCLIMinutes(t, ns, "Three", nil, t0, num(0), num(0), num(20), nil, num(20))
	host := func(h string) []map[string]string { return []map[string]string{{"Name": "Host", "Value": h}} }
	putCLIMinutes(t, ns, "Latency", host("web-1"), t0, num(5), num(7), nil, nil, nil)
	putCLIMinutes(t, ns, "Latency", host("web-2"), t0, num(50), num(70), nil, nil, nil)

	got := cliGetMetricData(t, []map[string]any{
		cliMetric("metric1", ns, "One"), cliMetric("metric2", ns, "Two"), cliMetric("metric3", ns, "Three"),
		{"Id": "tts", "Expression": "IF(metric1, metric2, metric3)"},
		{"Id": "filled", "Expression": "FILL(metric3, REPEAT)"},
		{"Id": "found", "Period": 60, "Expression": fmt.Sprintf(`SEARCH('{%s,Host} MetricName="Latency" NOT web-2', 'Maximum')`, ns)},
		{"Id": "q1", "Period": 60, "Expression": fmt.Sprintf(`SELECT MAX(Latency) FROM SCHEMA("%s", Host) GROUP BY Host ORDER BY MAX() DESC LIMIT 1`, ns)},
	}, t0, t0.Add(5*time.Minute))
	require.Len(t, got["tts"], 1)
	assert.Equal(t, []float64{30, 0, 20, 0}, got["tts"][0].Values, "IF(metric1, metric2, metric3) per the User Guide's table")
	require.Len(t, got["filled"], 1)
	assert.Equal(t, []float64{0, 0, 20, 20, 20}, got["filled"][0].Values)
	require.Len(t, got["found"], 1)
	assert.Equal(t, []float64{5, 7}, got["found"][0].Values)
	require.Len(t, got["q1"], 1)
	assert.Equal(t, "web-2", got["q1"][0].Label)
	assert.Equal(t, []float64{50, 70}, got["q1"][0].Values)

	errOut := runCLIExpectError(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", `[{"Id":"e1","Expression":"NO_SUCH_FUNCTION(1)"}]`,
		"--start-time", t0.Format(time.RFC3339), "--end-time", t0.Add(5*time.Minute).Format(time.RFC3339)))
	assert.Contains(t, errOut, "ValidationError")
}

// TestCloudWatchCLI_MetricMathAnomalyDetector creates a metric math anomaly
// detector with model settings, describes it by type and deletes it by id.
func TestCloudWatchCLI_MetricMathAnomalyDetector(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathAnomalyCLI")
	putCLIBaseline(t, ns, "Errors")
	queries := fmt.Sprintf(`{"MetricDataQueries":[{"Id":"m1","ReturnData":false,"MetricStat":{"Metric":{"Namespace":"%s","MetricName":"Errors"},"Period":60,"Stat":"Sum"}},{"Id":"e1","ReturnData":true,"Expression":"m1 * 2"}]}`, ns)
	out := runCLI(t, awsCLI("cloudwatch", "put-anomaly-detector", "--metric-math-anomaly-detector", queries,
		"--configuration", `{"AdaptationSpeed":"FAST","OutlierFilteringStrength":"MINIMAL"}`,
		"--metric-characteristics", `{"PeriodicSpikes":true}`, "--output", "json"))
	var put struct {
		AnomalyDetectorId string `json:"AnomalyDetectorId"`
	}
	parseJSON(t, out, &put)
	require.NotEmpty(t, put.AnomalyDetectorId)

	type described struct {
		AnomalyDetectors []struct {
			AnomalyDetectorId         string `json:"AnomalyDetectorId"`
			StateValue                string `json:"StateValue"`
			MetricMathAnomalyDetector struct {
				MetricDataQueries []struct {
					Id         string `json:"Id"`
					Expression string `json:"Expression"`
				} `json:"MetricDataQueries"`
			} `json:"MetricMathAnomalyDetector"`
			Configuration struct {
				AdaptationSpeed          string `json:"AdaptationSpeed"`
				OutlierFilteringStrength string `json:"OutlierFilteringStrength"`
			} `json:"Configuration"`
			MetricCharacteristics struct {
				PeriodicSpikes bool `json:"PeriodicSpikes"`
			} `json:"MetricCharacteristics"`
		} `json:"AnomalyDetectors"`
	}
	var d described
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors", "--anomaly-detector-ids", put.AnomalyDetectorId, "--output", "json")), &d)
		return len(d.AnomalyDetectors) == 1 && d.AnomalyDetectors[0].StateValue == "TRAINED"
	}, 30*time.Second, 500*time.Millisecond)
	det := d.AnomalyDetectors[0]
	require.Len(t, det.MetricMathAnomalyDetector.MetricDataQueries, 2)
	assert.Equal(t, "m1 * 2", det.MetricMathAnomalyDetector.MetricDataQueries[1].Expression)
	assert.Equal(t, "FAST", det.Configuration.AdaptationSpeed)
	assert.Equal(t, "MINIMAL", det.Configuration.OutlierFilteringStrength)
	assert.True(t, det.MetricCharacteristics.PeriodicSpikes)

	byType := runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors", "--anomaly-detector-types", "METRIC_MATH",
		"--query", fmt.Sprintf("length(AnomalyDetectors[?AnomalyDetectorId=='%s'])", put.AnomalyDetectorId), "--output", "text"))
	assert.Equal(t, "1", strings.TrimSpace(byType))

	errOut := runCLIExpectError(t, awsCLI("cloudwatch", "put-anomaly-detector", "--metric-math-anomaly-detector", queries,
		"--configuration", `{"AdaptationSpeed":"SOMETIMES"}`))
	assert.Contains(t, errOut, "InvalidParameterValue")

	runCLI(t, awsCLI("cloudwatch", "delete-anomaly-detector", "--anomaly-detector-id", put.AnomalyDetectorId))
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors", "--anomaly-detector-ids", put.AnomalyDetectorId, "--output", "json")), &d)
	assert.Empty(t, d.AnomalyDetectors)
}

// TestCloudWatchCLI_MetricWidgetOptions draws a right y-axis, a vertical
// annotation and the pie view through get-metric-widget-image.
func TestCloudWatchCLI_MetricWidgetOptions(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/WidgetCLI")
	putCLIBaseline(t, ns, "Load")
	render := func(widget string) image.Image {
		b64 := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-widget-image", "--metric-widget", widget,
			"--query", "MetricWidgetImage", "--output", "text")))
		raw, err := base64.StdEncoding.DecodeString(b64)
		require.NoError(t, err)
		img, err := png.Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		return img
	}
	count := func(img image.Image, r8, g8, b8 uint32) int {
		n := 0
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r>>8 == r8 && g>>8 == g8 && b>>8 == b8 {
					n++
				}
			}
		}
		return n
	}
	at := time.Now().UTC().Add(-25 * time.Minute).Format(time.RFC3339)
	img := render(fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#9467bd"}],[{"expression":"m1 * 1000","color":"#2ca02c","yAxis":"right","label":"scaled"}]],"period":60,"start":"-PT1H","width":360,"height":220,"annotations":{"vertical":[{"value":"%[2]s","label":"release","color":"#d62728"}]}}`, ns, at))
	assert.Greater(t, count(img, 0x94, 0x67, 0xbd), 50, "the left axis metric is drawn")
	assert.Greater(t, count(img, 0x2c, 0xa0, 0x2c), 50, "the right axis expression is drawn")
	assert.Greater(t, count(img, 0xd6, 0x27, 0x28), 100, "the vertical annotation is drawn")

	pie := render(fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#9467bd"}],[{"expression":"m1 * 3","color":"#2ca02c"}]],"view":"pie","period":60,"start":"-PT1H","width":240,"height":240,"legend":{"position":"hidden"}}`, ns))
	purple, green := count(pie, 0x94, 0x67, 0xbd), count(pie, 0x2c, 0xa0, 0x2c)
	require.Positive(t, purple)
	assert.InDelta(t, 3.0, float64(green)/float64(purple), 0.3, "a slice's angle is the series' share")
}
