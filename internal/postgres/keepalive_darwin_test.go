package postgres

import "syscall"

// keepAliveIdleOption is the option of the idle time of the keepalive,
// which macOS calls TCP_KEEPALIVE.
const keepAliveIdleOption = syscall.TCP_KEEPALIVE
