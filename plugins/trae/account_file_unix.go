//go:build unix

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func readAccountCredential(dir, name string) ([]byte, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid credential name")
	}
	parent, err := openOAuthDirectory(dir)
	if err != nil {
		return nil, fmt.Errorf("credential directory unavailable")
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("physical credential unavailable")
	}
	file := os.NewFile(uintptr(fd), "credential")
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Mode&0022 != 0 {
		return nil, fmt.Errorf("unsafe physical credential")
	}
	const maxCredentialBytes = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(raw) > maxCredentialBytes {
		return nil, fmt.Errorf("physical credential unreadable or too large")
	}
	return raw, nil
}

func writeAccountCredential(dir, name string, raw []byte) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid credential name")
	}
	parent, err := openOAuthDirectory(dir)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".credential-" + hex.EncodeToString(nonce[:]) + ".part"
	fd, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(parent, tmp, 0)
	file := os.NewFile(uintptr(fd), "credential-temp")
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(parent, tmp, parent, name); err != nil {
		return err
	}
	return unix.Fsync(parent)
}
