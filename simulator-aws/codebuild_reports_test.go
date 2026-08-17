package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report readers are exercised here against the raw result files real test
// and coverage frameworks write, so what DescribeTestCases and
// DescribeCodeCoverages answer with is what the files said. The end-to-end flow
// — a build writing these files and the two operations reading them back
// through the AWS SDK — is TestCodeBuild_ReportInsights_SDK.

const cbJUnitFixture = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="checkout" tests="4" failures="1" errors="1" skipped="1">
    <testcase classname="cart.CheckoutTest" name="totalsTheBasket" time="0.125"/>
    <testcase classname="cart.CheckoutTest" name="rejectsAnEmptyBasket" time="0.5">
      <failure message="expected 400, got 200">at CheckoutTest.java:42</failure>
    </testcase>
    <testcase classname="cart.CheckoutTest" name="chargesTheCard" time="1.5">
      <error message="connection refused"/>
    </testcase>
    <testcase classname="cart.CheckoutTest" name="appliesAVoucher" time="0">
      <skipped message="voucher service unavailable"/>
    </testcase>
  </testsuite>
</testsuites>`

func TestCodeBuildReports_JUnitXMLIsReadAsItsTestCases(t *testing.T) {
	cases, err := cbParseJUnitXML([]byte(cbJUnitFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cases) != 4 {
		t.Fatalf("read %d test cases, want 4: %+v", len(cases), cases)
	}
	want := []CBTestCase{
		{Prefix: "cart.CheckoutTest", Name: "totalsTheBasket", Status: "SUCCEEDED",
			TestSuiteName: "checkout", DurationInNanoSeconds: 125000000},
		{Prefix: "cart.CheckoutTest", Name: "rejectsAnEmptyBasket", Status: "FAILED",
			Message: "expected 400, got 200", TestSuiteName: "checkout", DurationInNanoSeconds: 500000000},
		{Prefix: "cart.CheckoutTest", Name: "chargesTheCard", Status: "ERROR",
			Message: "connection refused", TestSuiteName: "checkout", DurationInNanoSeconds: 1500000000},
		{Prefix: "cart.CheckoutTest", Name: "appliesAVoucher", Status: "SKIPPED",
			Message: "voucher service unavailable", TestSuiteName: "checkout"},
	}
	for i, expected := range want {
		if cases[i] != expected {
			t.Errorf("case %d = %+v, want %+v", i, cases[i], expected)
		}
	}
}

// A bare <testsuite> root is what a single-suite framework writes, and nested
// suites are what an aggregating one writes; both are JUnit XML.
func TestCodeBuildReports_JUnitXMLReadsABareAndANestedSuite(t *testing.T) {
	bare, err := cbParseJUnitXML([]byte(
		`<testsuite name="unit"><testcase classname="pkg.T" name="one" time="0.25"/></testsuite>`))
	if err != nil {
		t.Fatalf("parse bare suite: %v", err)
	}
	if len(bare) != 1 || bare[0].Name != "one" || bare[0].TestSuiteName != "unit" {
		t.Fatalf("bare suite read %+v", bare)
	}
	nested, err := cbParseJUnitXML([]byte(
		`<testsuites><testsuite name="outer"><testsuite name="inner">` +
			`<testcase classname="pkg.T" name="deep" time="0"/></testsuite></testsuite></testsuites>`))
	if err != nil {
		t.Fatalf("parse nested suite: %v", err)
	}
	if len(nested) != 1 || nested[0].Name != "deep" || nested[0].TestSuiteName != "inner" {
		t.Fatalf("nested suite read %+v", nested)
	}
}

const cbCucumberFixture = `[
  {"uri":"features/checkout.feature","name":"Checkout","elements":[
    {"type":"scenario","name":"A basket with one item","steps":[
      {"keyword":"Given ","name":"a basket","result":{"status":"passed","duration":1000000}},
      {"keyword":"Then ","name":"it totals","result":{"status":"passed","duration":2000000}}]},
    {"type":"scenario","name":"An empty basket","steps":[
      {"keyword":"Given ","name":"an empty basket","result":{"status":"passed","duration":1000000}},
      {"keyword":"Then ","name":"it is rejected","result":{"status":"failed","duration":3000000,
        "error_message":"expected 400, got 200"}}]},
    {"type":"scenario","name":"A voucher","steps":[
      {"keyword":"Given ","name":"a voucher","result":{"status":"undefined","duration":0}}]}]}
]`

func TestCodeBuildReports_CucumberJSONIsReadAsItsScenarios(t *testing.T) {
	cases, err := cbParseCucumberJSON([]byte(cbCucumberFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []CBTestCase{
		{Prefix: "Checkout", Name: "A basket with one item", Status: "SUCCEEDED",
			TestSuiteName: "Checkout", DurationInNanoSeconds: 3000000},
		{Prefix: "Checkout", Name: "An empty basket", Status: "FAILED",
			Message: "expected 400, got 200", TestSuiteName: "Checkout", DurationInNanoSeconds: 4000000},
		{Prefix: "Checkout", Name: "A voucher", Status: "ERROR",
			Message: "Given a voucher", TestSuiteName: "Checkout"},
	}
	if len(cases) != len(want) {
		t.Fatalf("read %d scenarios, want %d: %+v", len(cases), len(want), cases)
	}
	for i, expected := range want {
		if cases[i] != expected {
			t.Errorf("scenario %d = %+v, want %+v", i, cases[i], expected)
		}
	}
}

const cbJaCoCoFixture = `<?xml version="1.0" encoding="UTF-8"?>
<report name="cart">
  <package name="com/example/cart">
    <sourcefile name="Checkout.java">
      <counter type="BRANCH" missed="1" covered="3"/>
      <counter type="LINE" missed="2" covered="8"/>
    </sourcefile>
    <sourcefile name="Basket.java">
      <counter type="LINE" missed="0" covered="5"/>
    </sourcefile>
  </package>
</report>`

func TestCodeBuildReports_JaCoCoXMLIsReadAsPerFileCoverage(t *testing.T) {
	coverages, err := cbParseJaCoCoXML([]byte(cbJaCoCoFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []CBCodeCoverage{
		{FilePath: "com/example/cart/Checkout.java",
			LinesCovered: 8, LinesMissed: 2, LineCoveragePercentage: 80,
			BranchesCovered: 3, BranchesMissed: 1, BranchCoveragePercentage: 75},
		{FilePath: "com/example/cart/Basket.java",
			LinesCovered: 5, LinesMissed: 0, LineCoveragePercentage: 100},
	}
	if len(coverages) != len(want) {
		t.Fatalf("read %d files, want %d: %+v", len(coverages), len(want), coverages)
	}
	for i, expected := range want {
		if coverages[i] != expected {
			t.Errorf("file %d = %+v, want %+v", i, coverages[i], expected)
		}
	}
}

const cbCoberturaFixture = `<?xml version="1.0"?>
<coverage line-rate="0.75" branch-rate="0.5">
  <packages>
    <package name="cart">
      <classes>
        <class filename="cart/checkout.py" name="Checkout">
          <lines>
            <line number="1" hits="1"/>
            <line number="2" hits="0"/>
            <line number="3" hits="4" branch="true" condition-coverage="50% (1/2)"/>
          </lines>
        </class>
        <class filename="cart/checkout.py" name="Voucher">
          <lines>
            <line number="9" hits="2"/>
          </lines>
        </class>
      </classes>
    </package>
  </packages>
</coverage>`

func TestCodeBuildReports_CoberturaXMLAccumulatesTheClassesOfAFile(t *testing.T) {
	coverages, err := cbParseCoberturaXML([]byte(cbCoberturaFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(coverages) != 1 {
		t.Fatalf("read %d files, want 1: %+v", len(coverages), coverages)
	}
	want := CBCodeCoverage{
		FilePath: "cart/checkout.py", LinesCovered: 3, LinesMissed: 1, LineCoveragePercentage: 75,
		BranchesCovered: 1, BranchesMissed: 1, BranchCoveragePercentage: 50,
	}
	if coverages[0] != want {
		t.Errorf("coverage = %+v, want %+v", coverages[0], want)
	}
}

// A file that is not the format the buildspec declared is an error, not an
// empty report: answering with nothing would report a passing build with no
// tests where the raw data was there and unreadable.
func TestCodeBuildReports_AMisdeclaredFormatIsAnError(t *testing.T) {
	if _, err := cbParseJUnitXML([]byte(`<report name="cart"/>`)); err == nil {
		t.Error("a JaCoCo document read as JUNITXML returned no error")
	}
	if _, err := cbParseJaCoCoXML([]byte(`<testsuites/>`)); err == nil {
		t.Error("a JUnit document read as JACOCOXML returned no error")
	}
	if _, err := cbParseCoberturaXML([]byte(`<testsuites/>`)); err == nil {
		t.Error("a JUnit document read as COBERTURAXML returned no error")
	}
	if _, err := cbParseCucumberJSON([]byte(`<testsuites/>`)); err == nil {
		t.Error("an XML document read as CUCUMBERJSON returned no error")
	}
}

func TestCodeBuildReports_FilesPatternsMatchWhatAWSDocuments(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"my-test-report-file.json", "my-test-report-file.json", true},
		{"my-test-report-file.json", "sub/my-test-report-file.json", false},
		{"sub/report.xml", "sub/report.xml", true},
		{"**/*", "a/b/c/report.xml", true},
		{"**/*", "report.xml", true},
		{"sub/*", "sub/report.xml", true},
		{"sub/*", "sub/deeper/report.xml", false},
		{"sub/**/*", "sub/deeper/report.xml", true},
		{"sub/**/*", "sub/report.xml", true},
		{"sub/**/*", "other/report.xml", false},
	}
	for _, tc := range cases {
		if got := cbReportPathMatches(tc.pattern, tc.path); got != tc.want {
			t.Errorf("pattern %q against %q = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// A report whose files match nothing carries no results and settles INCOMPLETE
// — the status AWS documents for "the path to the test cases under the report
// group in the buildspec file might be incorrect".
func TestCodeBuildReports_NoMatchingFileIsIncompleteAndEmpty(t *testing.T) {
	workspace := t.TempDir()
	results, status, err := cbIngestReport(workspace,
		cbReportSpec{Key: "unit", Files: []string{"**/*"}, BaseDirectory: "test-results"},
		"arn:aws:codebuild:us-east-1:123456789012:report/probe:1")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if status != "INCOMPLETE" {
		t.Errorf("status = %q, want INCOMPLETE", status)
	}
	if len(results.TestCases) != 0 || len(results.CodeCoverages) != 0 {
		t.Errorf("a report with no raw data file carried %d test cases and %d coverage rows",
			len(results.TestCases), len(results.CodeCoverages))
	}
}

// Ingestion reads the files out of the build's own source directory, honouring
// base-directory and discard-paths, and settles the status the cases imply.
func TestCodeBuildReports_IngestsTheDeclaredFilesFromTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	reportDir := filepath.Join(workspace, "test-results", "surefire")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "junit.xml"), []byte(cbJUnitFixture), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	const arn = "arn:aws:codebuild:us-east-1:123456789012:report/probe:1"

	results, status, err := cbIngestReport(workspace, cbReportSpec{
		Key: "unit", Files: []string{"**/*"}, BaseDirectory: "test-results",
	}, arn)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// One case failed and one errored, so "Some of the test cases were not
	// successful".
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	if len(results.TestCases) != 4 {
		t.Fatalf("read %d test cases, want 4", len(results.TestCases))
	}
	for _, testCase := range results.TestCases {
		if testCase.ReportArn != arn {
			t.Errorf("case %q carries reportArn %q", testCase.Name, testCase.ReportArn)
		}
		if testCase.TestRawDataPath != "surefire/junit.xml" {
			t.Errorf("case %q carries testRawDataPath %q", testCase.Name, testCase.TestRawDataPath)
		}
	}

	discarded, _, err := cbIngestReport(workspace, cbReportSpec{
		Key: "unit", Files: []string{"**/*"}, BaseDirectory: "test-results", DiscardPaths: true,
	}, arn)
	if err != nil {
		t.Fatalf("ingest with discard-paths: %v", err)
	}
	if len(discarded.TestCases) == 0 || discarded.TestCases[0].TestRawDataPath != "/junit.xml" {
		t.Errorf("discard-paths kept the directory structure: %+v", discarded.TestCases)
	}
}

// A coverage report reads only coverage, and a test report only test cases: the
// two Describe operations answer for the report group's own type.
func TestCodeBuildReports_ACoverageReportCarriesNoTestCases(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "jacoco.xml"), []byte(cbJaCoCoFixture), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	const arn = "arn:aws:codebuild:us-east-1:123456789012:report/probe:2"
	results, status, err := cbIngestReport(workspace, cbReportSpec{
		Key: "coverage", Files: []string{"jacoco.xml"}, FileFormat: "jacocoxml",
	}, arn)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if status != "SUCCEEDED" {
		t.Errorf("status = %q, want SUCCEEDED", status)
	}
	if len(results.TestCases) != 0 {
		t.Errorf("a code coverage report carried %d test cases", len(results.TestCases))
	}
	if len(results.CodeCoverages) != 2 {
		t.Fatalf("read %d coverage rows, want 2", len(results.CodeCoverages))
	}
	if results.CodeCoverages[0].ID != arn+":com/example/cart/Checkout.java" {
		t.Errorf("coverage id = %q", results.CodeCoverages[0].ID)
	}
}

// A format AWS documents but this slice does not read fails with a message
// naming it, rather than producing an empty or an invented report.
func TestCodeBuildReports_AnUnreadFormatFailsByName(t *testing.T) {
	for _, format := range []string{"NUNITXML", "NUNIT3XML", "TESTNGXML", "VISUALSTUDIOTRX", "CLOVERXML", "SIMPLECOV", "LCOVINFO"} {
		_, _, err := cbIngestReport(t.TempDir(),
			cbReportSpec{Key: "unit", Files: []string{"**/*"}, FileFormat: format},
			"arn:aws:codebuild:us-east-1:123456789012:report/probe:3")
		if err == nil {
			t.Errorf("file-format %s produced a report instead of failing", format)
			continue
		}
		if got := err.Error(); !strings.Contains(got, format) {
			t.Errorf("file-format %s failed with %q, which does not name the format", format, got)
		}
	}
}

// A file-format AWS does not define is rejected before the build starts, the
// way the service rejects an invalid buildspec.
func TestCodeBuildReports_AnUndefinedFormatIsRejected(t *testing.T) {
	if err := cbValidateReportSpecs([]cbReportSpec{
		{Key: "unit", Files: []string{"**/*"}, FileFormat: "GOTESTJSON"},
	}); err == nil {
		t.Error("an undefined file-format was accepted")
	}
	if err := cbValidateReportSpecs([]cbReportSpec{{Key: "unit"}}); err == nil {
		t.Error("a reports entry with no files was accepted")
	}
	if err := cbValidateReportSpecs([]cbReportSpec{
		{Key: "unit", Files: []string{"**/*"}, FileFormat: "junitxml"},
	}); err != nil {
		t.Errorf("a lower-cased file-format was rejected: %v — the value is not case sensitive", err)
	}
}

// "A test report can have a maximum of 500 test case results. If more than 500
// test cases are run, CodeBuild prioritizes tests with the status FAILED and
// truncates the test case results."
func TestCodeBuildReports_TruncationKeepsTheFailures(t *testing.T) {
	cases := make([]CBTestCase, 0, 600)
	for i := range 600 {
		status := "SUCCEEDED"
		if i >= 590 {
			status = "FAILED"
		}
		cases = append(cases, CBTestCase{Name: string(rune('a'+i%26)) + string(rune('0'+i%10)), Status: status})
	}
	kept, truncated := cbTruncateTestCases(cases)
	if !truncated {
		t.Fatal("600 test cases were not reported as truncated")
	}
	if len(kept) != cbTestCaseLimit {
		t.Fatalf("kept %d cases, want %d", len(kept), cbTestCaseLimit)
	}
	failures := 0
	for _, testCase := range kept {
		if testCase.Status == "FAILED" {
			failures++
		}
	}
	if failures != 10 {
		t.Errorf("truncation kept %d of the 10 failures", failures)
	}
	short, truncatedShort := cbTruncateTestCases(cases[:10])
	if truncatedShort || len(short) != 10 {
		t.Errorf("a report under the ceiling was truncated")
	}
}
