package main
// sandboxSeccompProfile is applied to RUNTIME containers only.
// Compile containers need fork+execve for g++ to spawn cc1/as/ld.
// clone is intentionally ALLOWED — hidden_server.cpp uses std::thread.
const sandboxSeccompProfile = `{
    "defaultAction": "SCMP_ACT_ALLOW",
    "syscalls": [
        {
            "names": ["fork", "vfork", "execve", "execveat"],
            "action": "SCMP_ACT_KILL_PROCESS"
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