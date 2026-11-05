// Command demo runs the A ∥ B → C scenario against the Fake backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/engine"
	"projectos/kernel/internal/exec"
	"projectos/kernel/internal/store"
)

func main() {
	failA := flag.Bool("fail-a", false, "make task A produce an invalid result")
	dir := flag.String("dir", "", "state directory (default: temporary)")
	flag.Parse()

	root := *dir
	if root == "" {
		tmp, err := os.MkdirTemp("", "projectos-demo-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(tmp)
		root = tmp
	}
	st, err := store.Open(filepath.Join(root, "kernel.db"))
	if err != nil {
		panic(err)
	}
	defer st.Close()

	fake := exec.NewFake(1)
	fake.Output["A"], fake.Output["B"], fake.Output["C"] = "A-OK", "B-OK", "A-OK+B-OK"
	if *failA {
		fake.Output["A"] = "A-BROKEN"
	}
	checks := map[string]engine.ValidatorFunc{
		"exact:A": func(result string, _ map[domain.TaskID]string) error {
			if result != "A-OK" {
				return fmt.Errorf("unexpected artifact %q", result)
			}
			return nil
		},
		"exact:B": func(result string, _ map[domain.TaskID]string) error {
			if result != "B-OK" {
				return fmt.Errorf("unexpected artifact %q", result)
			}
			return nil
		},
		"sum": func(result string, deps map[domain.TaskID]string) error {
			if result != "A-OK+B-OK" {
				return fmt.Errorf("unexpected artifact %q", result)
			}
			return nil
		},
	}
	e := engine.New(st, fake, 2, checks)
	for _, task := range []domain.Task{
		{ID: "A", Prompt: "produce A", Validator: "exact:A"},
		{ID: "B", Prompt: "produce B", Validator: "exact:B"},
		{ID: "C", Prompt: "combine A and B", Validator: "sum", Deps: []domain.TaskID{"A", "B"}},
	} {
		if err := e.Add(task); err != nil {
			panic(err)
		}
	}

	steps, err := e.Run(context.Background(), 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine error: %v\n", err)
		os.Exit(1)
	}
	tasks, _ := st.List()
	events, _ := st.Events()

	fmt.Printf("steps=%d peak_concurrency=%d\n", steps, fake.PeakConcurrent())
	for _, t := range tasks {
		fmt.Printf("task %-2s status=%-10s attempts=%d %s\n", t.ID, t.Status, t.Attempts, t.LastError)
	}
	fmt.Println("events:")
	for _, ev := range events {
		fmt.Printf("  %-2s %-12s %s\n", ev.TaskID, ev.Kind, ev.Detail)
	}
}
