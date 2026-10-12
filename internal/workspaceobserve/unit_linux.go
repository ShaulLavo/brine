//go:build linux

package workspaceobserve

const UnitName = "brine-workspace-observer.service"
const UnitPath = "/etc/systemd/system/" + UnitName

// Unit preserves passed mount identity because mount namespaces would change the identity of a passed directory mount.
// The service therefore uses a capability/syscall sandbox without remounts.
const Unit = `[Unit]
Description=Observe Brine workspace consumers without mutation
After=systemd-user-sessions.service

[Service]
Type=simple
User=root
Group=brine
ExecStart=/usr/local/bin/brine host workspace-observer
WorkingDirectory=/
RuntimeDirectory=brine-workspace-observer
RuntimeDirectoryMode=0755
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
RestrictAddressFamilies=AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
SystemCallFilter=~@mount @reboot @swap @module @raw-io @obsolete ptrace process_vm_readv process_vm_writev kcmp kill pidfd_send_signal
MemoryMax=96M
TasksMax=16
LimitNOFILE=256
TimeoutStopSec=5s
Restart=on-failure
RestartSec=1s

[Install]
WantedBy=multi-user.target
`
