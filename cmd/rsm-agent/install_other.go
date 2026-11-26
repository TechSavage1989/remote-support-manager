//go:build !windows

package main

import (
	"context"
	"os"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
)

func install(bool) (string, error)                   { return os.Executable() }
func uninstall() error                               { return nil }
func removeAutostart() error                         { return nil }
func startDetached(string) error                     { return nil }
func singleInstance() bool                           { return true }
func setQuit(agentcore.Platform, context.CancelFunc) {}

func setUpdater(agentcore.Platform, *agentcore.Agent, context.CancelFunc) {}
func confirmStartup() bool                                                { return false }
func markHealthy()                                                        {}
