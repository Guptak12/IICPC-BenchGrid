package main

// sandboxSeccompProfile is applied to RUNTIME containers only.
// Compile containers need fork+execve for g++ to spawn cc1/as/ld.
// clone is intentionally ALLOWED — hidden_server.cpp uses std::thread.
//
// Socket syscall policy:
//   ALLOWED: socket, bind, listen, accept, accept4, recv, recvfrom,
//            send, sendto, sendmsg, recvmsg, setsockopt, getsockopt,
//            shutdown, getsockname, getpeername
//   BLOCKED: connect    — initiates outbound TCP/UDP connections
//            socketpair — creates a connected socket pair (exfil vector)
//
// connect() is the primary outbound vector. socketpair() is lower risk
// (fork is already KILL_PROCESS) but blocked for defense in depth.
//
// sendto() with a dest addr on a SOCK_DGRAM socket is a connectionless
// UDP exfil path that bypasses connect(). We do NOT block sendto() here
// because it is also the kernel path for send() on TCP sockets on some
// kernel versions. The sandbox-net Docker network (internal:true) is the
// second layer — UDP packets cannot route outside the bridge regardless.
const sandboxSeccompProfile = `{
    "defaultAction": "SCMP_ACT_ALLOW",
    "syscalls": [
        {
            "names": ["fork", "vfork", "execve", "execveat"],
            "action": "SCMP_ACT_KILL_PROCESS"
        },
        {
            "names": [
                "connect",
                "socketpair"
            ],
            "action": "SCMP_ACT_ERRNO"
        },
        {
            "names": [
                "ptrace",
                "personality",
                "setuid", "setgid",
                "setreuid", "setregid",
                "setresuid", "setresgid",
                "mount", "umount2",
                "pivot_root", "chroot",
                "syslog",
                "kexec_load", "kexec_file_load",
                "create_module", "init_module",
                "finit_module", "delete_module",
                "iopl", "ioperm"
            ],
            "action": "SCMP_ACT_ERRNO"
        }
    ]
}`