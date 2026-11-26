//go:build !windows

package main

import "errors"

var errWindowsOnly = errors.New("this command is for the Windows service; on other systems use \"rsm-server run\" with your service manager")

func cmdService([]string) error   { return errWindowsOnly }
func cmdInstall([]string) error   { return errWindowsOnly }
func cmdUninstall([]string) error { return errWindowsOnly }
