package db

import "errors"

// ErrDeviceTaskHistory prevents deleting a device whose task history is part
// of the durable audit trail for manual or campaign work.
var ErrDeviceTaskHistory = errors.New("device has task history")
