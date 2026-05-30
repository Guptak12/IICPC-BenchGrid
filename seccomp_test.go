// seccomp_test.go
package main

import (
    "encoding/json"
    "testing"
)

func TestSeccompProfileIsValidJSON(t *testing.T) {
    var profile map[string]interface{}
    if err := json.Unmarshal([]byte(sandboxSeccompProfile), &profile); err != nil {
        t.Fatalf("sandboxSeccompProfile is not valid JSON: %v", err)
    }
}

func TestSeccompProfileBlocksConnect(t *testing.T) {
    var profile struct {
        Syscalls []struct {
            Names  []string `json:"names"`
            Action string   `json:"action"`
        } `json:"syscalls"`
    }
    if err := json.Unmarshal([]byte(sandboxSeccompProfile), &profile); err != nil {
        t.Fatal(err)
    }

    blocked := make(map[string]string) // syscall name → action
    for _, rule := range profile.Syscalls {
        for _, name := range rule.Names {
            blocked[name] = rule.Action
        }
    }

    mustBlock := []string{"connect", "socketpair", "fork", "execve"}
    for _, name := range mustBlock {
        action, ok := blocked[name]
        if !ok {
            t.Errorf("syscall %q has no rule — it will be ALLOWED by default", name)
            continue
        }
        if action != "SCMP_ACT_ERRNO" && action != "SCMP_ACT_KILL_PROCESS" {
            t.Errorf("syscall %q has action %q — expected ERRNO or KILL_PROCESS", name, action)
        }
    }

    // send/recv must NOT be blocked — server needs them for bot connections
    mustAllow := []string{"send", "recv", "sendto", "recvfrom", "accept", "bind", "listen"}
    for _, name := range mustAllow {
        action, ok := blocked[name]
        if ok && (action == "SCMP_ACT_ERRNO" || action == "SCMP_ACT_KILL_PROCESS") {
            t.Errorf("syscall %q is blocked but must be allowed — server needs it", name)
        }
    }
}