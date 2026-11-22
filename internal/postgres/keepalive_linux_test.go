package postgres

import "syscall"

// keepAliveIdleOption is the option of the idle time of the keepalive.
const keepAliveIdleOption = syscall.TCP_KEEPIDLE
