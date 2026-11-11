package storage

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func privateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	// Replace inherited permissions; allow the runtime account, SYSTEM and
	// Administrators only. Directories pass these permissions to new children.
	return windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;BA)(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")")
}

func protectDirectory(path string) error {
	// Applying a private inherited ACL to an entire drive would affect unrelated
	// files. A data store must be a dedicated subdirectory.
	if filepath.Dir(path) == path {
		return errors.New("Windows 数据目录不能是卷根目录，请使用专用子目录")
	}
	sd, err := privateDescriptor(true)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func protectFile(f *os.File) error {
	sd, err := privateDescriptor(false)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// os.CreateTemp's handle does not request WRITE_DAC. Set by name inside the
	// already-private directory rather than assuming GENERIC_WRITE includes it.
	return windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func replaceFile(source, dest string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	// The temp file is in the destination directory and has already been flushed
	// and secured. Never delete the old file first or fall back to copy/truncate.
	err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	if err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: dest, Err: err}
	}
	return nil
}
