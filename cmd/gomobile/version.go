// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/baguettex/gomobile/internal/sdkpath"
)

var cmdVersion = &command{
	run:   runVersion,
	Name:  "version",
	Usage: "",
	Short: "print version",
	Long: `
Version prints versions of the gomobile binary and tools
`,
}

func runVersion(cmd *command) (err error) {
	// Check this binary matches the version in github.com/baguettex/gomobile/cmd/gomobile
	// source code in GOPATH. If they don't match, currently there is no
	// way to reliably identify the revision number this binary was built
	// against.
	version, err := func() (string, error) {
		bin, err := exec.LookPath(os.Args[0])
		if err != nil {
			return "", err
		}
		bindir := filepath.Dir(bin)
		cmd := exec.Command("go", "list", "-f", "{{.Stale}}", "github.com/baguettex/gomobile/cmd/gomobile")
		cmd.Env = append(os.Environ(), "GOBIN="+bindir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("cannot test gomobile binary: %v, %s", err, out)
		}
		if strings.TrimSpace(string(out)) != "false" {
			return "", fmt.Errorf("binary is out of date, re-install it")
		}
		return mobileRepoRevision()
	}()
	if err != nil {
		fmt.Printf("gomobile version unknown: %v\n", err)
		return nil
	}

	// Supported platforms
	platforms := "android"
	if xcodeAvailable() {
		platforms += "," + strings.Join(applePlatforms, ",")
	}

	androidapi, _ := sdkpath.AndroidAPIPath(buildAndroidAPI)

	fmt.Printf("gomobile version %s (%s); androidSDK=%s\n", version, platforms, androidapi)
	return nil
}

func mobileRepoRevision() (rev string, err error) {
	b, err := exec.Command("go", "list", "-f", "{{.Dir}}", "github.com/baguettex/gomobile/app").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mobile repo not found: %v, %s", err, b)
	}

	repo := filepath.Dir(string(b))
	if err := os.Chdir(repo); err != nil {
		return "", fmt.Errorf("mobile repo %q not accessible: %v", repo, err)
	}
	revision, err := exec.Command("git", "log", "-n", "1", "--format=format: +%h %cd", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mobile repo git log failed: %v, %s", err, revision)
	}
	return string(bytes.Trim(revision, " \t\r\n")), nil
}
