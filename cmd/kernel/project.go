package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdProject(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kernel project <list|snapshot|decision|context> [flags]")
		return exitError
	}
	switch args[0] {
	case "list":
		return projectList(args[1:])
	case "snapshot":
		return projectSnapshot(args[1:])
	case "decision":
		return projectDecision(args[1:])
	case "context":
		return projectContext(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown project command %q\n", args[0])
		return exitError
	}
}

func projectList(args []string) int {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	dir := fs.String("dir", "", "workspace directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	ws := newWorkspace(*dir)
	list, err := ws.Projects()
	if err != nil {
		fmt.Fprintln(os.Stderr, "projects:", err)
		return exitError
	}
	for _, p := range list {
		fmt.Printf("%-24s %s\n", p.ID, p.Name)
	}
	return exitOK
}

func projectSnapshot(args []string) int {
	fs := flag.NewFlagSet("project snapshot", flag.ContinueOnError)
	dir := fs.String("dir", "", "workspace directory")
	id := fs.String("project", "", "project id")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "snapshot requires --project")
		return exitError
	}
	ws := newWorkspace(*dir)
	snap, err := ws.ProjectSnapshot(*id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return exitError
	}
	fmt.Printf("project=%s objectives=%d verified=%d blockers=%d hash=%s\n",
		snap.ProjectID, len(snap.Objectives), len(snap.Verified), len(snap.Blockers), snap.Hash[:12])
	fmt.Printf("written to %s\n", filepath.Join(ws.root, "project-state", snap.ProjectID, ".project"))
	return exitOK
}

func projectContext(args []string) int {
	fs := flag.NewFlagSet("project context", flag.ContinueOnError)
	dir := fs.String("dir", "", "workspace directory")
	id := fs.String("project", "", "project id")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "context requires --project")
		return exitError
	}
	fmt.Print(newWorkspace(*dir).ProjectContextText(*id))
	return exitOK
}

func projectDecision(args []string) int {
	fs := flag.NewFlagSet("project decision", flag.ContinueOnError)
	dir := fs.String("dir", "", "workspace directory")
	id := fs.String("project", "", "project id")
	text := fs.String("text", "", "decision text")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if strings.TrimSpace(*id) == "" || strings.TrimSpace(*text) == "" {
		fmt.Fprintln(os.Stderr, "decision requires --project and --text")
		return exitError
	}
	ws := newWorkspace(*dir)
	if err := ws.appendProjectDecision(*id, *text); err != nil {
		fmt.Fprintln(os.Stderr, "decision:", err)
		return exitError
	}
	if _, err := ws.ProjectSnapshot(*id); err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return exitError
	}
	fmt.Println("project decision recorded")
	return exitOK
}
