package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jfrog/jfrog-cli-core/v2/utils/config"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	xrutils "github.com/jfrog/jfrog-cli-core/v2/xray/utils"
	"github.com/jfrog/jfrog-cli/utils/tests"
	"github.com/jfrog/jfrog-client-go/auth"
	clientutils "github.com/jfrog/jfrog-client-go/utils"
	"github.com/jfrog/jfrog-client-go/utils/io/fileutils"
	"github.com/jfrog/jfrog-client-go/utils/io/httputils"
	"github.com/jfrog/jfrog-client-go/utils/version"
	"github.com/jfrog/jfrog-client-go/xray/services"
	"github.com/stretchr/testify/assert"
)

var (
	xrayDetails     *config.ServerDetails
	xrayAuth        auth.ServiceDetails
	xrayHttpDetails httputils.HttpClientDetails
	// JFrog CLI for Xray commands
	xrayCli *tests.JfrogCli
)

func InitXrayTests() {
	initXrayCli()
}

func authenticateXray() string {
	*tests.JfrogUrl = clientutils.AddTrailingSlashIfNeeded(*tests.JfrogUrl)
	xrayDetails = &config.ServerDetails{XrayUrl: *tests.JfrogUrl + tests.XrayEndpoint}
	cred := fmt.Sprintf("--url=%s", xrayDetails.XrayUrl)
	if *tests.JfrogAccessToken != "" {
		xrayDetails.AccessToken = *tests.JfrogAccessToken
		cred += fmt.Sprintf(" --access-token=%s", xrayDetails.AccessToken)
	} else {
		xrayDetails.User = *tests.JfrogUser
		xrayDetails.Password = *tests.JfrogPassword
		cred += fmt.Sprintf(" --user=%s --password=%s", xrayDetails.User, xrayDetails.Password)
	}

	var err error
	if xrayAuth, err = xrayDetails.CreateXrayAuthConfig(); err != nil {
		coreutils.ExitOnErr(errors.New("Failed while attempting to authenticate with Xray: " + err.Error()))
	}
	xrayDetails.XrayUrl = xrayAuth.GetUrl()
	xrayHttpDetails = xrayAuth.CreateHttpClientDetails()
	return cred
}

func initXrayCli() {
	if xrayCli != nil {
		return
	}
	cred := authenticateXray()
	xrayCli = tests.NewJfrogCli(execMain, "jfrog", cred)
}

// Tests basic binary scan by providing pattern (path to testdata binaries) and --licenses flag
// and asserts any error.
func TestXrayBinaryScan(t *testing.T) {
	initXrayTest(t, xrutils.GraphScanMinVersion)
	binariesPath := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "xray", "binaries", "*")
	output := runAuditCmdWithOutput(t, "scan", binariesPath, "--licenses", "--format=json")
	verifyScanResults(t, output, 0, 1, 1)
}

// Tests npm audit by providing simple npm project and asserts any error.
func TestXrayAuditNpm(t *testing.T) {
	initXrayTest(t, xrutils.GraphScanMinVersion)
	tempDirPath, err := fileutils.CreateTempDir()
	assert.NoError(t, err)
	defer tests.RemoveTempDirAndAssert(t, tempDirPath)
	npmProjectPath := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "xray", "npm")
	// Copy the npm project from the testdata to a temp dir
	assert.NoError(t, fileutils.CopyDir(npmProjectPath, tempDirPath, true, nil))
	prevWd := changeWD(t, tempDirPath)
	defer tests.ChangeDirAndAssert(t, prevWd)
	// Run npm install before executing jfrog xr npm-audit
	assert.NoError(t, exec.Command("npm", "install").Run())

	output := runAuditCmdWithOutput(t, "audit-npm", "--licenses", "--format=json")
	verifyScanResults(t, output, 0, 1, 1)
}

func TestXrayAuditGradle(t *testing.T) {
	initXrayTest(t, xrutils.GraphScanMinVersion)
	tempDirPath, err := fileutils.CreateTempDir()
	assert.NoError(t, err)
	defer tests.RemoveTempDirAndAssert(t, tempDirPath)
	gradleProjectPath := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "xray", "gradle")
	// Copy the gradle project from the testdata to a temp dir
	assert.NoError(t, fileutils.CopyDir(gradleProjectPath, tempDirPath, true, nil))
	prevWd := changeWD(t, tempDirPath)
	defer tests.ChangeDirAndAssert(t, prevWd)

	output := runAuditCmdWithOutput(t, "audit-gradle", "--licenses", "--format=json")
	verifyScanResults(t, output, 0, 0, 0)
}

func TestXrayAuditMaven(t *testing.T) {
	initXrayTest(t, xrutils.GraphScanMinVersion)
	tempDirPath, err := fileutils.CreateTempDir()
	assert.NoError(t, err)
	defer tests.RemoveTempDirAndAssert(t, tempDirPath)
	mvnProjectPath := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "xray", "maven")
	// Copy the maven project from the testdata to a temp dir
	assert.NoError(t, fileutils.CopyDir(mvnProjectPath, tempDirPath, true, nil))
	prevWd := changeWD(t, tempDirPath)
	defer tests.ChangeDirAndAssert(t, prevWd)
	output := runAuditCmdWithOutput(t, "audit-mvn", "--licenses", "--format=json")
	verifyScanResults(t, output, 0, 1, 1)
}

func initXrayTest(t *testing.T, minVersion ...string) {
	if !*tests.TestXray {
		t.Skip("Skipping Xray test. To run Xray test add the '-test.xray=true' option.")
	}
	xrayVersion, err := getXrayVersion()
	if err != nil {
		assert.NoError(t, err)
		return
	}
	// If minimal version was supplied, make sure the Xray version fulfil the minimum version requirement
	if len(minVersion) > 0 && !xrayVersion.AtLeast(minVersion[0]) {
		t.Skip(fmt.Sprintf("Skipping Xray test. You are using Xray %s, while  this test requires Xray version %s or higher.", xrayVersion, minVersion))
	}
}

func getXrayVersion() (version.Version, error) {
	xrayVersion, err := xrayAuth.GetVersion()
	return *version.NewVersion(xrayVersion), err
}

// Run `jfrog` command, redirect the stdout and return the output
func runAuditCmdWithOutput(t *testing.T, args ...string) string {
	newStdout, stdWriter, previousStdout := tests.RedirectStdOutToPipe()
	// Restore previous stdout when the function returns
	defer func() {
		os.Stdout = previousStdout
		newStdout.Close()
	}()
	go func() {
		err := xrayCli.Exec(args...)
		assert.NoError(t, err)
		// Closing the temp stdout in order to be able to read it's content.
		stdWriter.Close()
	}()
	content, err := ioutil.ReadAll(newStdout)
	assert.NoError(t, err)
	// Prints the redirected output to the standard output as well.
	previousStdout.Write(content)
	return string(content)
}

func verifyScanResults(t *testing.T, content string, minViolations, minVulnerabilities, minLicenses int) {
	var results []services.ScanResponse
	err := json.Unmarshal([]byte(content), &results)
	assert.NoError(t, err)
	assert.True(t, len(results[0].Violations) >= minViolations, fmt.Sprintf("Expected at least %d violations in scan results, but got %d violations.", minViolations, len(results[0].Violations)))
	assert.True(t, len(results[0].Vulnerabilities) >= minVulnerabilities, fmt.Sprintf("Expected at least %d vulnerabilities in scan results, but got %d vulnerabilities.", minVulnerabilities, len(results[0].Vulnerabilities)))
	assert.True(t, len(results[0].Licenses) >= minLicenses, fmt.Sprintf("Expected at least %d Licenses in scan results, but got %d Licenses.", minLicenses, len(results[0].Licenses)))
}
