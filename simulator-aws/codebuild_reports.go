package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// AWS CodeBuild reports — reading the raw result files a build produced.
//
// A build's buildspec declares its reports in the `reports:` section, keyed by
// report-group name or ARN, each naming the files that hold the raw results and
// the format they are written in:
//
//	reports:
//	  {{report-group-name-or-arn}}:
//	    files:
//	      - {{location}}
//	    base-directory: {{location}}
//	    discard-paths: no | yes
//	    file-format: {{report-format}}
//
// The service reads those files out of the build environment and reports what
// they contain. `file-format` is "Optional mapping. Represents the report file
// format. If not specified, JUNITXML is used. This value is not case
// sensitive." (Build specification reference for CodeBuild.)
//
// Formats this AWS service slice reads:
//
//	JUNITXML       JUnit XML          test cases
//	CUCUMBERJSON   Cucumber JSON      test cases
//	JACOCOXML      JaCoCo XML         code coverage
//	COBERTURAXML   Cobertura XML      code coverage
//
// Formats AWS documents that this slice does NOT read: NUNITXML, NUNIT3XML,
// TESTNGXML and VISUALSTUDIOTRX (test), SIMPLECOV, CLOVERXML and LCOVINFO
// (code coverage). A build that declares one of them fails with a message
// naming it, rather than producing an empty or an invented report.
//
// Statuses follow the service's published meanings (Test report statuses):
// SUCCEEDED "All test cases were successful", FAILED "Some of the test cases
// were not successful", and INCOMPLETE "The test report was not completed …
// the path to the test cases under the report group in the buildspec file
// might be incorrect" — which is what a `files` pattern matching nothing is.
// A test case's own status is one of SUCCEEDED, FAILED, ERROR, SKIPPED or
// UNKNOWN, where UNKNOWN is "The test case returned a status other than
// SUCCEEDED, FAILED, ERROR, or SKIPPED".
//
// "A test report can have a maximum of 500 test case results. If more than 500
// test cases are run, CodeBuild prioritizes tests with the status FAILED and
// truncates the test case results." — cbTestCaseLimit below.

const cbTestCaseLimit = 500

// cbReportFormats maps every file-format token AWS documents to the report type
// it produces, and whether this slice reads it.
var cbReportFormats = map[string]struct {
	reportType string
	read       bool
}{
	"CUCUMBERJSON":    {"TEST", true},
	"JUNITXML":        {"TEST", true},
	"NUNITXML":        {"TEST", false},
	"NUNIT3XML":       {"TEST", false},
	"TESTNGXML":       {"TEST", false},
	"VISUALSTUDIOTRX": {"TEST", false},
	"CLOVERXML":       {"CODE_COVERAGE", false},
	"COBERTURAXML":    {"CODE_COVERAGE", true},
	"JACOCOXML":       {"CODE_COVERAGE", true},
	"SIMPLECOV":       {"CODE_COVERAGE", false},
	"LCOVINFO":        {"CODE_COVERAGE", false},
}

// CBTestCase mirrors the CodeBuild TestCase shape.
type CBTestCase struct {
	ReportArn             string `json:"reportArn"`
	TestRawDataPath       string `json:"testRawDataPath"`
	Prefix                string `json:"prefix,omitempty"`
	Name                  string `json:"name"`
	Status                string `json:"status"`
	DurationInNanoSeconds int64  `json:"durationInNanoSeconds"`
	Message               string `json:"message,omitempty"`
	TestSuiteName         string `json:"testSuiteName,omitempty"`
}

// CBCodeCoverage mirrors the CodeBuild CodeCoverage shape.
type CBCodeCoverage struct {
	ID                       string  `json:"id"`
	ReportARN                string  `json:"reportARN"`
	FilePath                 string  `json:"filePath"`
	LineCoveragePercentage   float64 `json:"lineCoveragePercentage"`
	LinesCovered             int     `json:"linesCovered"`
	LinesMissed              int     `json:"linesMissed"`
	BranchCoveragePercentage float64 `json:"branchCoveragePercentage"`
	BranchesCovered          int     `json:"branchesCovered"`
	BranchesMissed           int     `json:"branchesMissed"`
}

// CBReportResults holds what one report's raw data files contained. It is
// stored under the report's ARN, which is how DescribeTestCases and
// DescribeCodeCoverages address it.
type CBReportResults struct {
	Arn           string           `json:"arn"`
	TestCases     []CBTestCase     `json:"testCases"`
	CodeCoverages []CBCodeCoverage `json:"codeCoverages"`
}

// cbReportSpec is one entry of the buildspec `reports:` section.
type cbReportSpec struct {
	// Key is the report-group-name-or-arn exactly as the buildspec wrote it.
	Key           string   `yaml:"-" json:"key"`
	Files         []string `yaml:"files" json:"files"`
	BaseDirectory string   `yaml:"base-directory" json:"baseDirectory"`
	DiscardPaths  bool     `yaml:"discard-paths" json:"discardPaths"`
	FileFormat    string   `yaml:"file-format" json:"fileFormat"`
}

// format returns the spec's effective file-format token, upper-cased. AWS:
// "If not specified, JUNITXML is used. This value is not case sensitive."
func (s cbReportSpec) format() string {
	if s.FileFormat == "" {
		return "JUNITXML"
	}
	return strings.ToUpper(strings.TrimSpace(s.FileFormat))
}

// cbValidateReportSpecs rejects a buildspec whose reports section names a
// file-format AWS does not define, the way the service rejects an invalid
// buildspec before the build starts.
func cbValidateReportSpecs(specs []cbReportSpec) error {
	for _, spec := range specs {
		if _, ok := cbReportFormats[spec.format()]; !ok {
			return fmt.Errorf("reports.%s.file-format %q is not a CodeBuild report format", spec.Key, spec.FileFormat)
		}
		if len(spec.Files) == 0 {
			return fmt.Errorf("reports.%s.files is required", spec.Key)
		}
	}
	return nil
}

// cbReportGroupForSpec resolves the report group a spec names, creating it when
// the spec named a new group. AWS: "Specify the ARN of an existing report
// group, or the name of a new report group. If you specify a name, CodeBuild
// creates a report group using your project name and the name you specify in
// the format <project-name>-<report-group-name>." Caller holds cbMu.
func cbReportGroupForSpec(spec cbReportSpec, projectName string) (CBReportGroup, error) {
	reportType := cbReportFormats[spec.format()].reportType
	if strings.HasPrefix(spec.Key, "arn:") {
		group, ok := cbReportGrps.Get(spec.Key)
		if !ok {
			return CBReportGroup{}, fmt.Errorf("report group not found: %s", spec.Key)
		}
		return group, nil
	}
	name := projectName + "-" + spec.Key
	arn := cbARN("report-group/" + name)
	if group, ok := cbReportGrps.Get(arn); ok {
		return group, nil
	}
	now := cbEpochNow()
	group := CBReportGroup{
		Arn:          arn,
		Name:         name,
		Type:         reportType,
		Created:      now,
		LastModified: now,
		Status:       "ACTIVE",
	}
	cbReportGrps.Put(arn, group)
	return group, nil
}

// cbCollectReportFiles returns the raw data files a report spec matches inside
// the build workspace, as workspace-relative slash paths, sorted.
//
// AWS: files entries are "relative to the original build location or, if set,
// the base-directory", and base-directory "Represents one or more top-level
// directories, relative to the original build location, that CodeBuild uses to
// determine where to find the raw test files."
func cbCollectReportFiles(workspace string, spec cbReportSpec) ([]string, error) {
	root := workspace
	if spec.BaseDirectory != "" {
		root = filepath.Join(workspace, filepath.FromSlash(spec.BaseDirectory))
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		// A base-directory that the build never produced is exactly the
		// "path … might be incorrect" configuration problem, so no file matches.
		return nil, nil
	}
	var matched []string
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, pattern := range spec.Files {
			if cbReportPathMatches(pattern, rel) {
				matched = append(matched, rel)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(matched)
	return matched, nil
}

// cbReportPathMatches applies one buildspec files pattern to a relative path.
// AWS documents a single wildcard beyond path.Match's: "'**/*' represents all
// files recursively" and "{{my-subdirectory}}/**/* represents all files
// recursively starting from a subdirectory", so "**" matches any run of path
// segments including none.
func cbReportPathMatches(pattern, rel string) bool {
	return cbGlobSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func cbGlobSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(name); skip++ {
			if cbGlobSegments(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
		return false
	}
	return cbGlobSegments(pattern[1:], name[1:])
}

// cbReportRawDataPath is the path the service records for a raw result file:
// the file's path under the report's base directory, flattened to its base name
// when the spec sets discard-paths. AWS: "If this contains yes, all of the test
// files are placed in the same output directory. For example, if a path to a
// test result is com/myapp/mytests/TestResult.xml, specifying yes will place
// this file in /TestResult.xml."
func cbReportRawDataPath(spec cbReportSpec, rel string) string {
	if spec.DiscardPaths {
		return "/" + path.Base(rel)
	}
	return rel
}

// cbIngestReport reads a report spec's raw data files out of the build
// workspace and returns the results plus the report status those results imply.
func cbIngestReport(workspace string, spec cbReportSpec, reportArn string) (CBReportResults, string, error) {
	format := spec.format()
	definition, ok := cbReportFormats[format]
	if !ok {
		return CBReportResults{}, "", fmt.Errorf("reports.%s.file-format %q is not a CodeBuild report format", spec.Key, spec.FileFormat)
	}
	if !definition.read {
		return CBReportResults{}, "", fmt.Errorf(
			"reports.%s declares file-format %s, which this AWS service slice does not read; "+
				"it reads JUNITXML and CUCUMBERJSON test reports and JACOCOXML and COBERTURAXML code coverage reports",
			spec.Key, format)
	}
	files, err := cbCollectReportFiles(workspace, spec)
	if err != nil {
		return CBReportResults{}, "", err
	}
	results := CBReportResults{Arn: reportArn}
	for _, rel := range files {
		root := workspace
		if spec.BaseDirectory != "" {
			root = filepath.Join(workspace, filepath.FromSlash(spec.BaseDirectory))
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return CBReportResults{}, "", err
		}
		rawPath := cbReportRawDataPath(spec, rel)
		switch format {
		case "JUNITXML":
			cases, err := cbParseJUnitXML(data)
			if err != nil {
				return CBReportResults{}, "", fmt.Errorf("read %s as JUNITXML: %w", rel, err)
			}
			results.TestCases = append(results.TestCases, cbStampTestCases(cases, reportArn, rawPath)...)
		case "CUCUMBERJSON":
			cases, err := cbParseCucumberJSON(data)
			if err != nil {
				return CBReportResults{}, "", fmt.Errorf("read %s as CUCUMBERJSON: %w", rel, err)
			}
			results.TestCases = append(results.TestCases, cbStampTestCases(cases, reportArn, rawPath)...)
		case "JACOCOXML":
			coverages, err := cbParseJaCoCoXML(data)
			if err != nil {
				return CBReportResults{}, "", fmt.Errorf("read %s as JACOCOXML: %w", rel, err)
			}
			results.CodeCoverages = append(results.CodeCoverages, cbStampCoverages(coverages, reportArn)...)
		case "COBERTURAXML":
			coverages, err := cbParseCoberturaXML(data)
			if err != nil {
				return CBReportResults{}, "", fmt.Errorf("read %s as COBERTURAXML: %w", rel, err)
			}
			results.CodeCoverages = append(results.CodeCoverages, cbStampCoverages(coverages, reportArn)...)
		}
	}
	if len(files) == 0 {
		// "INCOMPLETE: The test report was not completed. … A problem with the
		// configuration of the report group … the path to the test cases under
		// the report group in the buildspec file might be incorrect."
		return results, "INCOMPLETE", nil
	}
	if definition.reportType == "CODE_COVERAGE" {
		return results, "SUCCEEDED", nil
	}
	status := "SUCCEEDED"
	for _, testCase := range results.TestCases {
		if testCase.Status != "SUCCEEDED" && testCase.Status != "SKIPPED" {
			status = "FAILED"
			break
		}
	}
	return results, status, nil
}

// cbTruncateTestCases applies the service's 500-case ceiling: "CodeBuild
// prioritizes tests with the status FAILED and truncates the test case
// results." Returns the retained cases and whether truncation happened.
func cbTruncateTestCases(cases []CBTestCase) ([]CBTestCase, bool) {
	if len(cases) <= cbTestCaseLimit {
		return cases, false
	}
	kept := make([]CBTestCase, 0, cbTestCaseLimit)
	for _, testCase := range cases {
		if testCase.Status == "FAILED" && len(kept) < cbTestCaseLimit {
			kept = append(kept, testCase)
		}
	}
	for _, testCase := range cases {
		if testCase.Status == "FAILED" || len(kept) >= cbTestCaseLimit {
			continue
		}
		kept = append(kept, testCase)
	}
	return kept, true
}

func cbStampTestCases(cases []CBTestCase, reportArn, rawPath string) []CBTestCase {
	for i := range cases {
		cases[i].ReportArn = reportArn
		cases[i].TestRawDataPath = rawPath
	}
	return cases
}

func cbStampCoverages(coverages []CBCodeCoverage, reportArn string) []CBCodeCoverage {
	for i := range coverages {
		coverages[i].ReportARN = reportArn
		coverages[i].ID = reportArn + ":" + coverages[i].FilePath
	}
	return coverages
}

// ----- JUnit XML -------------------------------------------------------------

type cbJUnitTestSuites struct {
	XMLName xml.Name           `xml:"testsuites"`
	Suites  []cbJUnitTestSuite `xml:"testsuite"`
}

type cbJUnitTestSuite struct {
	XMLName xml.Name           `xml:"testsuite"`
	Name    string             `xml:"name,attr"`
	Suites  []cbJUnitTestSuite `xml:"testsuite"`
	Cases   []cbJUnitTestCase  `xml:"testcase"`
}

type cbJUnitTestCase struct {
	Name      string          `xml:"name,attr"`
	ClassName string          `xml:"classname,attr"`
	Time      string          `xml:"time,attr"`
	Failure   *cbJUnitOutcome `xml:"failure"`
	Error     *cbJUnitOutcome `xml:"error"`
	Skipped   *cbJUnitOutcome `xml:"skipped"`
}

type cbJUnitOutcome struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// cbParseJUnitXML reads a JUnit XML result file. The document root is either a
// <testsuites> wrapper or a bare <testsuite>, both of which real frameworks
// emit; a suite may nest further suites.
func cbParseJUnitXML(data []byte) ([]CBTestCase, error) {
	var suites cbJUnitTestSuites
	if err := xml.Unmarshal(data, &suites); err == nil && suites.XMLName.Local == "testsuites" {
		var out []CBTestCase
		for _, suite := range suites.Suites {
			out = append(out, cbJUnitSuiteCases(suite)...)
		}
		return out, nil
	}
	var suite cbJUnitTestSuite
	if err := xml.Unmarshal(data, &suite); err != nil {
		return nil, err
	}
	if suite.XMLName.Local != "testsuite" {
		return nil, fmt.Errorf("root element is <%s>, not <testsuites> or <testsuite>", suite.XMLName.Local)
	}
	return cbJUnitSuiteCases(suite), nil
}

func cbJUnitSuiteCases(suite cbJUnitTestSuite) []CBTestCase {
	var out []CBTestCase
	for _, nested := range suite.Suites {
		out = append(out, cbJUnitSuiteCases(nested)...)
	}
	for _, testCase := range suite.Cases {
		status := "SUCCEEDED"
		message := ""
		switch {
		case testCase.Failure != nil:
			status, message = "FAILED", cbJUnitOutcomeMessage(testCase.Failure)
		case testCase.Error != nil:
			status, message = "ERROR", cbJUnitOutcomeMessage(testCase.Error)
		case testCase.Skipped != nil:
			status, message = "SKIPPED", cbJUnitOutcomeMessage(testCase.Skipped)
		}
		out = append(out, CBTestCase{
			// The prefix is "a string that is applied to a series of related
			// test cases … The prefix depends on the framework used" — for
			// JUnit XML that grouping is the case's classname.
			Prefix:                testCase.ClassName,
			Name:                  testCase.Name,
			Status:                status,
			Message:               message,
			TestSuiteName:         suite.Name,
			DurationInNanoSeconds: cbSecondsToNanoseconds(testCase.Time),
		})
	}
	return out
}

func cbJUnitOutcomeMessage(outcome *cbJUnitOutcome) string {
	if outcome.Message != "" {
		return outcome.Message
	}
	return strings.TrimSpace(outcome.Body)
}

func cbSecondsToNanoseconds(seconds string) int64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(seconds), 64)
	if err != nil || value < 0 {
		return 0
	}
	return int64(value * 1e9)
}

// ----- Cucumber JSON ---------------------------------------------------------

type cbCucumberFeature struct {
	Name     string              `json:"name"`
	URI      string              `json:"uri"`
	Elements []cbCucumberElement `json:"elements"`
}

type cbCucumberElement struct {
	Name  string           `json:"name"`
	Type  string           `json:"type"`
	Steps []cbCucumberStep `json:"steps"`
}

type cbCucumberStep struct {
	Keyword string `json:"keyword"`
	Name    string `json:"name"`
	Result  struct {
		Status       string `json:"status"`
		Duration     int64  `json:"duration"`
		ErrorMessage string `json:"error_message"`
	} `json:"result"`
}

// cbParseCucumberJSON reads a Cucumber JSON result file: a list of features,
// each holding scenario elements whose steps carry the outcome. A scenario is
// one test case — it fails on its first failing step, errors on an undefined or
// ambiguous one, and is skipped when no step ran. Cucumber reports step
// duration in nanoseconds, which is the unit TestCase already uses.
func cbParseCucumberJSON(data []byte) ([]CBTestCase, error) {
	var features []cbCucumberFeature
	if err := json.Unmarshal(data, &features); err != nil {
		return nil, err
	}
	var out []CBTestCase
	for _, feature := range features {
		for _, element := range feature.Elements {
			if element.Type != "" && element.Type != "scenario" {
				continue
			}
			status := "SKIPPED"
			message := ""
			var duration int64
			ran := false
			for _, step := range element.Steps {
				duration += step.Result.Duration
				switch step.Result.Status {
				case "passed":
					ran = true
				case "failed":
					ran = true
					if status != "FAILED" && status != "ERROR" {
						status = "FAILED"
						message = cbCucumberStepMessage(step)
					}
				case "undefined", "ambiguous", "pending":
					ran = true
					if status != "FAILED" && status != "ERROR" {
						status = "ERROR"
						message = cbCucumberStepMessage(step)
					}
				case "skipped":
				default:
					ran = true
					if status != "FAILED" && status != "ERROR" {
						status = "UNKNOWN"
						message = cbCucumberStepMessage(step)
					}
				}
			}
			if ran && status == "SKIPPED" {
				status = "SUCCEEDED"
			}
			out = append(out, CBTestCase{
				Prefix:                feature.Name,
				Name:                  element.Name,
				Status:                status,
				Message:               message,
				TestSuiteName:         feature.Name,
				DurationInNanoSeconds: duration,
			})
		}
	}
	return out, nil
}

func cbCucumberStepMessage(step cbCucumberStep) string {
	if step.Result.ErrorMessage != "" {
		return step.Result.ErrorMessage
	}
	return strings.TrimSpace(step.Keyword + step.Name)
}

// ----- JaCoCo XML ------------------------------------------------------------

type cbJaCoCoReport struct {
	XMLName  xml.Name          `xml:"report"`
	Packages []cbJaCoCoPackage `xml:"package"`
}

type cbJaCoCoPackage struct {
	Name        string               `xml:"name,attr"`
	SourceFiles []cbJaCoCoSourceFile `xml:"sourcefile"`
}

type cbJaCoCoSourceFile struct {
	Name     string            `xml:"name,attr"`
	Counters []cbJaCoCoCounter `xml:"counter"`
}

type cbJaCoCoCounter struct {
	Type    string `xml:"type,attr"`
	Missed  int    `xml:"missed,attr"`
	Covered int    `xml:"covered,attr"`
}

// cbParseJaCoCoXML reads a JaCoCo XML coverage report. Per-file coverage lives
// in each package's <sourcefile> counters: the LINE counter gives lines covered
// and missed, the BRANCH counter the conditional branches.
func cbParseJaCoCoXML(data []byte) ([]CBCodeCoverage, error) {
	var report cbJaCoCoReport
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	if report.XMLName.Local != "report" {
		return nil, fmt.Errorf("root element is <%s>, not <report>", report.XMLName.Local)
	}
	var out []CBCodeCoverage
	for _, pkg := range report.Packages {
		for _, file := range pkg.SourceFiles {
			coverage := CBCodeCoverage{FilePath: path.Join(pkg.Name, file.Name)}
			for _, counter := range file.Counters {
				switch counter.Type {
				case "LINE":
					coverage.LinesCovered = counter.Covered
					coverage.LinesMissed = counter.Missed
				case "BRANCH":
					coverage.BranchesCovered = counter.Covered
					coverage.BranchesMissed = counter.Missed
				}
			}
			out = append(out, cbWithCoveragePercentages(coverage))
		}
	}
	return out, nil
}

// ----- Cobertura XML ---------------------------------------------------------

type cbCoberturaCoverage struct {
	XMLName  xml.Name             `xml:"coverage"`
	Packages []cbCoberturaPackage `xml:"packages>package"`
}

type cbCoberturaPackage struct {
	Classes []cbCoberturaClass `xml:"classes>class"`
}

type cbCoberturaClass struct {
	Filename string            `xml:"filename,attr"`
	Lines    []cbCoberturaLine `xml:"lines>line"`
}

type cbCoberturaLine struct {
	Hits              int    `xml:"hits,attr"`
	Branch            string `xml:"branch,attr"`
	ConditionCoverage string `xml:"condition-coverage,attr"`
}

// cbParseCoberturaXML reads a Cobertura XML coverage report. Each class names
// the source file it covers and lists its lines; a line is covered when it was
// hit, and a branch line's condition-coverage attribute carries the covered and
// total branch counts as "NN% (covered/total)". Several classes may cover the
// same file (one per class in the file), so their line and branch counts
// accumulate per file.
func cbParseCoberturaXML(data []byte) ([]CBCodeCoverage, error) {
	var report cbCoberturaCoverage
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	if report.XMLName.Local != "coverage" {
		return nil, fmt.Errorf("root element is <%s>, not <coverage>", report.XMLName.Local)
	}
	byFile := map[string]*CBCodeCoverage{}
	var order []string
	for _, pkg := range report.Packages {
		for _, class := range pkg.Classes {
			coverage, seen := byFile[class.Filename]
			if !seen {
				coverage = &CBCodeCoverage{FilePath: class.Filename}
				byFile[class.Filename] = coverage
				order = append(order, class.Filename)
			}
			for _, line := range class.Lines {
				if line.Hits > 0 {
					coverage.LinesCovered++
				} else {
					coverage.LinesMissed++
				}
				if line.Branch != "true" {
					continue
				}
				covered, total, ok := cbCoberturaConditionCounts(line.ConditionCoverage)
				if !ok {
					continue
				}
				coverage.BranchesCovered += covered
				coverage.BranchesMissed += total - covered
			}
		}
	}
	out := make([]CBCodeCoverage, 0, len(order))
	for _, filename := range order {
		out = append(out, cbWithCoveragePercentages(*byFile[filename]))
	}
	return out, nil
}

// cbCoberturaConditionCounts reads the "(covered/total)" tail of a Cobertura
// condition-coverage attribute, e.g. "50% (1/2)".
func cbCoberturaConditionCounts(attr string) (int, int, bool) {
	open := strings.Index(attr, "(")
	closing := strings.Index(attr, ")")
	if open < 0 || closing < open {
		return 0, 0, false
	}
	covered, total, found := strings.Cut(attr[open+1:closing], "/")
	if !found {
		return 0, 0, false
	}
	coveredValue, err := strconv.Atoi(strings.TrimSpace(covered))
	if err != nil {
		return 0, 0, false
	}
	totalValue, err := strconv.Atoi(strings.TrimSpace(total))
	if err != nil || totalValue < coveredValue {
		return 0, 0, false
	}
	return coveredValue, totalValue, true
}

// cbWithCoveragePercentages fills the two percentage members from the counts.
// AWS: "line coverage = (total lines covered)/(total number of lines)" and
// "branch coverage = (total branches covered)/(total number of branches)".
func cbWithCoveragePercentages(coverage CBCodeCoverage) CBCodeCoverage {
	if lines := coverage.LinesCovered + coverage.LinesMissed; lines > 0 {
		coverage.LineCoveragePercentage = float64(coverage.LinesCovered) * 100 / float64(lines)
	}
	if branches := coverage.BranchesCovered + coverage.BranchesMissed; branches > 0 {
		coverage.BranchCoveragePercentage = float64(coverage.BranchesCovered) * 100 / float64(branches)
	}
	return coverage
}
