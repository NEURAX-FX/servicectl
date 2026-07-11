package dbusactivation

const (
	daemonHelperGenericFailure     = 1
	daemonHelperNoMemory           = 2
	daemonHelperConfigInvalid      = 3
	daemonHelperSetupFailed        = 4
	daemonHelperNameInvalid        = 5
	daemonHelperServiceNotFound    = 6
	daemonHelperPermissionsInvalid = 7
	daemonHelperFileInvalid        = 8
	daemonHelperExecFailed         = 9
	daemonHelperInvalidArgs        = 10
	daemonHelperChildSignaled      = 11
)

func DaemonHelperExitCode(code ResultCode) int {
	switch code {
	case ResultSuccess:
		return 0
	case ResultOutOfMemory:
		return daemonHelperNoMemory
	case ResultConfigInvalid:
		return daemonHelperConfigInvalid
	case ResultSetupFailed:
		return daemonHelperSetupFailed
	case ResultInvalidBusName:
		return daemonHelperNameInvalid
	case ResultUnknownService, ResultUnitNotFound, ResultServiceNotFound:
		return daemonHelperServiceNotFound
	case ResultPermissionsInvalid:
		return daemonHelperPermissionsInvalid
	case ResultFileInvalid:
		return daemonHelperFileInvalid
	case ResultExecFailed:
		return daemonHelperExecFailed
	case ResultInvalidArguments:
		return daemonHelperInvalidArgs
	case ResultChildSignaled:
		return daemonHelperChildSignaled
	default:
		return daemonHelperGenericFailure
	}
}
