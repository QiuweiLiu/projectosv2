package ao

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type proc struct {
	pid  int
	ppid int
	cmd  string
}

func listProcs() ([]proc, error) {
	out, err := exec.Command("ps", "-ax", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, proc{pid: pid, ppid: ppid, cmd: strings.Join(fields[2:], " ")})
	}
	return procs, nil
}

// Reap terminates AO's detached chat host for one session and every process it
// spawned. The host survives daemon shutdown by design, so the Kernel must
// clean it up explicitly.
func Reap(sessionID, dataDir string) ([]int, error) {
	if sessionID == "" {
		return nil, nil
	}
	procs, err := listProcs()
	if err != nil {
		return nil, err
	}
	var roots []int
	for _, p := range procs {
		if matchesChatHost(p.cmd, sessionID, dataDir) {
			roots = append(roots, p.pid)
		}
	}
	victims := withDescendants(roots, procs)
	terminate(victims)
	return victims, nil
}

// ReapAll terminates every AO chat host bound to one data directory. Used at
// shutdown, where individual session ids may be unknown.
func ReapAll(dataDir string) ([]int, error) {
	if dataDir == "" {
		return nil, nil
	}
	procs, err := listProcs()
	if err != nil {
		return nil, err
	}
	var roots []int
	for _, p := range procs {
		if matchesChatHost(p.cmd, "", dataDir) {
			roots = append(roots, p.pid)
		}
	}
	victims := withDescendants(roots, procs)
	terminate(victims)
	return victims, nil
}

// matchesChatHost matches the exact argv tokens `chat-host <session> <dataDir>`
// without splitting on whitespace: a data dir may itself contain spaces, and ps
// prints argv space-joined with no quoting. The trailing space boundary keeps
// /tmp/run-1 from matching /tmp/run-10.
func matchesChatHost(cmd, sessionID, dataDir string) bool {
	const marker = "chat-host "
	for offset := 0; ; {
		i := strings.Index(cmd[offset:], marker)
		if i < 0 {
			return false
		}
		i += offset
		if i > 0 && cmd[i-1] != ' ' {
			offset = i + len(marker)
			continue
		}
		rest := cmd[i+len(marker):]
		// The session token never contains a space; consume it explicitly so the
		// ReapAll mode (empty sessionID) lands on the data dir as well.
		if sessionID != "" {
			if !strings.HasPrefix(rest, sessionID+" ") {
				offset = i + len(marker)
				continue
			}
			rest = rest[len(sessionID)+1:]
		} else {
			sp := strings.IndexByte(rest, ' ')
			if sp < 0 {
				offset = i + len(marker)
				continue
			}
			rest = rest[sp+1:]
		}
		if dataDir == "" {
			return true
		}
		if !strings.HasPrefix(rest, dataDir) {
			offset = i + len(marker)
			continue
		}
		// A data dir that is a prefix of a longer one ("/tmp/project" vs
		// "/tmp/project old") must not match: AO follows the data dir with the
		// session worktree, which lives underneath it.
		if strings.HasPrefix(rest[len(dataDir):], " "+dataDir+"/") {
			return true
		}
		offset = i + len(marker)
	}
}

func withDescendants(roots []int, procs []proc) []int {
	children := map[int][]int{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	seen := map[int]bool{}
	var walk func(int)
	walk = func(pid int) {
		if seen[pid] || pid == 1 {
			return
		}
		seen[pid] = true
		for _, child := range children[pid] {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	out := make([]int, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	return out
}

func terminate(pids []int) {
	if len(pids) == 0 {
		return
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(alive(pids)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range alive(pids) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func alive(pids []int) []int {
	var out []int
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil {
			out = append(out, pid)
		}
	}
	return out
}
