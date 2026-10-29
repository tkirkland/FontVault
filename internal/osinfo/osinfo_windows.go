//go:build windows

// Package osinfo reports the host OS name and version.
package osinfo

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
	defer k.Close()
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
