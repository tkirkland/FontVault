//go:build windows

package osinfo

// Package OS info Package OS info reports the host OS name and version.

import (
	"golang.org/x/sys/windows/registry"
)

// String returns a human-readable OS name and version, e.g.
// "Windows 11 Pro (10.0.26100)".
func String() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer closeKeyQuietly(k)
	name, _, _ := k.GetStringValue("ProductName")
	if name == "" {
		name = "Windows"
	}
	display, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuildNumber")
	if display != "" {
		return name + " " + display
	}
	if build != "" {
		return name + " (build " + build + ")"
	}
	return name
}

// closeKeyQuietly closes a registry key handle: the values have already been
// read, so a close failure changes nothing (the best effort).
func closeKeyQuietly(k registry.Key) {
	_ = k.Close()
}
