//go:build darwin

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const (
	darwinPrivateLockHelperMode = "__darwin-service-lock-helper"
	darwinPrivateWatchdogMode   = "__darwin-service-watchdog"

	darwinServiceChildMarkerEnv = "KENT_PRIVATE_DARWIN_SERVICE_CHILD"
	darwinServiceLockFDEnv      = "KENT_PRIVATE_DARWIN_SERVICE_LOCK_FD"
	darwinServiceLeaseFDEnv     = "KENT_PRIVATE_DARWIN_SERVICE_LEASE_FD"
	darwinServiceGateFDEnv      = "KENT_PRIVATE_DARWIN_SERVICE_GATE_FD"

	darwinInheritedFDBase = 3
)

type darwinServiceMessage struct {
	Kind         string   `json:"kind"`
	HostPID      int      `json:"host_pid,omitempty"`
	HostCommand  []string `json:"host_command,omitempty"`
	ChildPID     int      `json:"child_pid,omitempty"`
	ChildCommand []string `json:"child_command,omitempty"`
}

func darwinSocketPair() ([2]int, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return [2]int{}, err
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	return pair, nil
}

func darwinSocketFile(fd int, name string) *os.File {
	return os.NewFile(uintptr(fd), name)
}

func writeDarwinServiceMessage(file *os.File, message darwinServiceMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) > 64*1024 {
		return errors.New("Darwin service control message is too large")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	if err := writeDarwinServiceBytes(file, header); err != nil {
		return err
	}
	return writeDarwinServiceBytes(file, payload)
}

func readDarwinServiceMessage(file *os.File) (darwinServiceMessage, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(file, header); err != nil {
		if errors.Is(err, io.EOF) {
			return darwinServiceMessage{}, ioEOF
		}
		return darwinServiceMessage{}, err
	}
	length := binary.BigEndian.Uint32(header)
	if length == 0 || length > 64*1024 {
		return darwinServiceMessage{}, fmt.Errorf("invalid Darwin service control message length %d", length)
	}
	buffer := make([]byte, int(length))
	_, err := io.ReadFull(file, buffer)
	if err != nil {
		return darwinServiceMessage{}, err
	}
	var message darwinServiceMessage
	if err := json.Unmarshal(buffer, &message); err != nil {
		return darwinServiceMessage{}, fmt.Errorf("decode Darwin service control message: %w", err)
	}
	return message, nil
}

var ioEOF = errors.New("Darwin service control channel closed")

func writeDarwinServiceBytes(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func closeDarwinFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
