package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	loggerTestEnv    = "MCS_TEST_CONTROLLER_RUNTIME_LOGGER"
	loggerTestMarker = "controller-runtime logger initialized"
)

func TestControllerRuntimeLoggerInitialized(t *testing.T) {
	if os.Getenv(loggerTestEnv) == "true" {
		log.Log.Info(loggerTestMarker)
		klog.Flush()
		return
	}

	// Run the assertion in a subprocess so the test exercises package
	// initialization without mutating controller-runtime's global logger.
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerRuntimeLoggerInitialized$")
	cmd.Env = append(os.Environ(), loggerTestEnv+"=true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logger test subprocess failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), loggerTestMarker) {
		t.Fatalf("controller-runtime log was not routed to klog:\n%s", output)
	}
}
