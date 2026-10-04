package main

import (
	"github.com/godbus/dbus/v5"
)

const (
	busName   = "dev.local.AntigravityUsage"
	objPath   = dbus.ObjectPath("/dev/local/AntigravityUsage")
	ifaceName = "dev.local.AntigravityUsage"
)

// Service implements the dev.local.AntigravityUsage D-Bus interface.
type Service struct {
	mgr *Manager
}

// GetSources returns all sources, primary first.
func (s *Service) GetSources() ([]Source, *dbus.Error) {
	return s.mgr.Snapshot(), nil
}

// Refresh triggers an immediate poll of every source.
func (s *Service) Refresh() *dbus.Error {
	s.mgr.RefreshAll()
	return nil
}
