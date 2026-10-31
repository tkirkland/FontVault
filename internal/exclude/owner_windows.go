//go:build windows

package exclude

import (
	"fmt"

	"golang.org/x/sys/windows"

	"fontvault/internal/fsutil"
)

// trustedInstallerSID is NT SERVICE\TrustedInstaller.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

type ownerClassifier struct {
	trustedInstaller *windows.SID
}

func newOSClassifier(_ func(format string, args ...any)) OSClassifier {
	ti, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		// Cannot happen for a constant SID; fail closed below.
		return &ownerClassifier{}
	}
	return &ownerClassifier{trustedInstaller: ti}
}

func (c *ownerClassifier) Name() string { return "owner SID" }

// IsOSDefault reads the file owner with GetNamedSecurityInfo and reports
// whether it is NT SERVICE\TrustedInstaller. Any failure means the status
// cannot be determined.
func (c *ownerClassifier) IsOSDefault(path string) (bool, error) {
	if c.trustedInstaller == nil {
		return false, fmt.Errorf("TrustedInstaller SID unavailable")
	}
	sd, err := windows.GetNamedSecurityInfo(fsutil.LongPath(path),
		windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read owner of %s: %w", path, err)
	}
	if sd == nil {
		return false, fmt.Errorf("read owner of %s: no security descriptor", path)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, fmt.Errorf("read owner of %s: %w", path, err)
	}
	if owner == nil {
		return false, fmt.Errorf("read owner of %s: no owner SID", path)
	}
	return windows.EqualSid(owner, c.trustedInstaller), nil
}
