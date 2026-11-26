//go:build !windows

package main

import (
	"context"
	"errors"
	"os"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

func install(bool) (string, error)                   { return os.Executable() }
func uninstall() error                               { return nil }
func removeAutostart() error                         { return nil }
func removeUserInstall(bool)                         {}
func startDetached(string, ...string) error          { return nil }
func singleInstance() bool                           { return true }
func setQuit(agentcore.Platform, context.CancelFunc) {}

func setUpdater(agentcore.Platform, *agentcore.Agent, context.CancelFunc) {}
func confirmStartup() bool                                                { return false }
func markHealthy()                                                        {}

var errWindowsOnly = errors.New("unattended access is available on Windows only")

func startAccessInstall(agentcore.Platform, *protocol.ClientFile, string, *agentcore.InviteInfo) error {
	return errWindowsOnly
}
func cmdInstallAccess([]string) error   { return errWindowsOnly }
func cmdUninstallAccess([]string) error { return errWindowsOnly }
func cmdService([]string) error         { return errWindowsOnly }
func cmdSessionHelper([]string) error   { return errWindowsOnly }
func cmdNotice([]string) error          { return errWindowsOnly }
