package dispatch

import "time"

const RequestTimeout = 15 * time.Second
const ReviewWorkTimeout = 9 * time.Minute
const ReviewCleanupTimeout = 70 * time.Second
const SSHConnectTimeout = 5 * time.Second
const NativeMaterialPullTimeout = 2 * time.Minute
const NativeMaterialWorkTimeout = NativeMaterialPullTimeout + RequestTimeout
const NativeMaterialCleanupTimeout = 5 * time.Second

func OperationTimeout(operation string) time.Duration {
	switch operation {
	case "module_publish_plan", "native_backup_inspect", "native_backup_review":
		return ReviewWorkTimeout
	case "native_material_set":
		return NativeMaterialWorkTimeout
	default:
		return RequestTimeout
	}
}

func CallTimeout(operation string) time.Duration {
	if operation == "native_material_set" {
		return RequestTimeout + NativeMaterialWorkTimeout + NativeMaterialCleanupTimeout + SSHConnectTimeout
	}
	work := OperationTimeout(operation)
	if work == RequestTimeout {
		return RequestTimeout
	}
	return RequestTimeout + work + ReviewCleanupTimeout + SSHConnectTimeout
}
