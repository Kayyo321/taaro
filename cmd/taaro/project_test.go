package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExistingAndNewProjects(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := setupProject("new", []string{"--repo", "example/widgets", "widgets"}); err != nil {
		t.Fatal(err)
	}
	if err := setupProject("new", []string{"--repo", "example/widgets", "widgets"}); err == nil {
		t.Fatal("accepted an existing destination")
	}
	if err := exec.Command("git", "init", "-q", "existing").Run(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"widgets", "existing"} {
		root := filepath.Join(dir, target)
		sub := filepath.Join(root, "src")
		if err := os.Mkdir(sub, 0755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(sub)
		for range 2 {
			if err := setupProject("init", []string{"--repo", "example/widgets"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := setupProject("init", []string{"--repo", "example/other"}); err == nil {
			t.Fatal("replaced conflicting project")
		}
		p, err := readProject(root)
		if err != nil || p.Repo != "example/widgets" {
			t.Fatalf("invalid project %+v: %v", p, err)
		}
		if err := os.WriteFile(filepath.Join(root, projectFile), []byte("custom data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := setupProject("init", []string{"--repo", "example/widgets"}); err == nil {
			t.Fatal("replaced malformed config")
		}
	}
}
