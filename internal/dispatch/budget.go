package dispatch

import "time"

const RequestTimeout = 15 * time.Second
const ReviewWorkTimeout = 9 * time.Minute
const ReviewCleanupTimeout = 70 * time.Second
const SSHConnectTimeout = 5 * time.Second

func OperationTimeout(operation string) time.Duration {
	switch operation {
	case "module_publish_plan", "native_backup_inspect", "native_backup_review":
		return ReviewWorkTimeout
	default:
		return RequestTimeout
	}
}

func CallTimeout(operation string) time.Duration {
	work := OperationTimeout(operation)
	if work == RequestTimeout {
		return RequestTimeout
	}
	return RequestTimeout + work + ReviewCleanupTimeout + SSHConnectTimeout
}
