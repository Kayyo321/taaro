package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const projectFile = ".taaro.json"

type project struct {
	Repo string `json:"repo"`
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

func validRepo(repo string) bool {
	return repoPattern.MatchString(repo) && !strings.HasSuffix(repo, "/.") && !strings.HasSuffix(repo, "/..")
}

func projectRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locate Git worktree in %s: %w", dir, err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func readProject(root string) (project, error) {
	var p project
	path := filepath.Join(root, projectFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil || !validRepo(p.Repo) {
		return p, fmt.Errorf("%s must contain a repo in owner/repo format", path)
	}
	return p, nil
}

func saveProject(root, repo string) error {
	data, err := json.MarshalIndent(project{repo}, "", "  ")
	if err != nil {
		return err
	}
	// Publish the complete file exclusively so concurrent initializers cannot
	// overwrite a maintainer's configuration or observe a partial write.
	tmp, err := os.CreateTemp(root, ".taaro-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	path := filepath.Join(root, projectFile)
	if err := os.Link(tmp.Name(), path); errors.Is(err, os.ErrExist) {
		p, readErr := readProject(root)
		if readErr != nil {
			return readErr
		}
		if p.Repo != repo {
			return fmt.Errorf("%s already selects %s", path, p.Repo)
		}
	} else if err != nil {
		return err
	}
	return nil
}

func setupProject(command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	repo := fs.String("repo", "", "GitHub repository in owner/repo format")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !validRepo(*repo) {
		return fmt.Errorf("--repo requires a GitHub owner/repo")
	}
	dir := "."
	if command == "new" {
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: taaro new --repo owner/repo DIRECTORY")
		}
		dir = fs.Arg(0)
		if err := os.Mkdir(dir, 0755); err != nil {
			return err
		}
		cmd := exec.Command("git", "init", "--quiet")
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			_ = os.Remove(dir) // Only remove an empty directory.
			return fmt.Errorf("initialize Git repository: %w: %s", err, output)
		}
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: taaro init --repo owner/repo")
	}
	root, err := projectRoot(dir)
	if err != nil {
		return err
	}
	if err := saveProject(root, *repo); err != nil {
		return err
	}
	fmt.Printf("Configured %s for %s\n", filepath.Join(root, projectFile), *repo)
	return nil
}
