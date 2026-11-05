package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"projectos/kernel/internal/store"
)

// pruneWork keeps the newest N attempt directories. The kernel deliberately
// keeps each attempt separate, but without a bound a long-lived state directory
// grows forever. A directory with a live .kernel-run.pid is never removed: an
// operator may run prune while another objective in the same project is busy.
func pruneWork(dir string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	root := filepath.Join(dir, "work")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	type item struct {
		name string
		mod  int64
	}
	var dirs []item
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, item{name: entry.Name(), mod: info.ModTime().UnixNano()})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod > dirs[j].mod })
	removed := 0
	for i, item := range dirs {
		if i < keep {
			continue
		}
		full := filepath.Join(root, item.name)
		if workDirBusy(full) {
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func workDirBusy(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, ".kernel-run.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	return processAlive(pid)
}

func goalPrune(args []string) int {
	fs := flag.NewFlagSet("goal prune", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory")
	keep := fs.Int("keep", 20, "keep the newest N work directories (0 = keep all)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "prune requires --dir")
		return exitError
	}
	// A read-only handle is enough: pruning files must not take the scheduler
	// lock or trigger recovery.
	st, err := store.OpenReadOnly(filepath.Join(*dir, "kernel.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return exitError
	}
	st.Close()
	removed, err := pruneWork(*dir, *keep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prune:", err)
		return exitError
	}
	fmt.Printf("pruned %d work directories (kept %d)\n", removed, *keep)
	return exitOK
}
