package dbusactivation

import "testing"

func TestDaemonHelperExitCodeMapping(t *testing.T) {
	tests := []struct {
		result ResultCode
		want   int
	}{
		{ResultSuccess, 0},
		{ResultOutOfMemory, 2},
		{ResultConfigInvalid, 3},
		{ResultSetupFailed, 4},
		{ResultInvalidBusName, 5},
		{ResultUnknownService, 6},
		{ResultUnitNotFound, 6},
		{ResultPermissionsInvalid, 7},
		{ResultFileInvalid, 8},
		{ResultExecFailed, 9},
		{ResultInvalidArguments, 10},
		{ResultChildSignaled, 11},
		{ResultChildExited, 1},
		{ResultTimeout, 1},
		{ResultBackendUnavailable, 1},
		{ResultProtocolError, 1},
		{ResultVersionMismatch, 1},
	}
	for _, tt := range tests {
		if got := DaemonHelperExitCode(tt.result); got != tt.want {
			t.Fatalf("DaemonHelperExitCode(%d) = %d, want %d", tt.result, got, tt.want)
		}
	}
}
