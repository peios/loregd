package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func notificationSocket(t *testing.T) (*net.UnixConn, string) {
	t.Helper()
	// Keep socket paths short even when the test's directory is deeply nested.
	directory, err := os.MkdirTemp("", "loregd-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(directory, "notify")
	receiver, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() }) // Tests may close it early to release a writer.
	return receiver, path
}

func TestSendReadyDeliversNotification(t *testing.T) {
	receiver, path := notificationSocket(t)
	if err := sendReady(path, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var message [64]byte
	n, _, err := receiver.ReadFromUnix(message[:])
	if err != nil {
		t.Fatal(err)
	}
	if got := string(message[:n]); got != "READY=1\n" {
		t.Fatalf("notification = %q, want READY=1 followed by newline", got)
	}
}

// fillNotificationQueue fills the receiver, not just one sender's smaller send
// buffer. A fresh sender timing out on its first datagram proves queue pressure.
func fillNotificationQueue(t *testing.T, path string) {
	t.Helper()
	for index := range 64 {
		address := &net.UnixAddr{Name: fmt.Sprintf("%s-%d", path, index), Net: "unixgram"}
		sender, err := net.ListenUnixgram("unixgram", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sender.Close() }) // The receiver may already have closed.
		if err := sender.SetWriteDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		for count := 0; ; count++ {
			_, err := sender.WriteToUnix([]byte("queued"), &net.UnixAddr{Name: path, Net: "unixgram"})
			if err == nil {
				continue
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal(err)
			}
			if count == 0 {
				return
			}
			break
		}
	}
	t.Fatal("could not fill notification receiver queue")
}

func TestSendReadyTimesOutWhenSupervisorStopsReading(t *testing.T) {
	receiver, path := notificationSocket(t)
	fillNotificationQueue(t, path)
	done := make(chan error, 1)
	go func() { done <- sendReady(path, 40*time.Millisecond) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("notification error = %v, want a deadline error", err)
		}
	case <-time.After(2 * time.Second):
		// Release a stuck writer before failing, so the regression itself cannot leak it.
		_ = receiver.Close()
		<-done
		t.Fatal("readiness notification stayed blocked beyond its deadline")
	}
}

type watchedDevice struct {
	closed chan struct{}
}

func (device *watchedDevice) Close() error {
	close(device.closed)
	return nil
}

func TestShutdownWatcherStopsWithoutClosingDevice(t *testing.T) {
	signals := make(chan os.Signal, 1)
	device := &watchedDevice{closed: make(chan struct{})}
	stop := startShutdownWatcher(signals, device)
	stop()
	// stop joins the watcher, so this later signal must not affect the device.
	signals <- syscall.SIGTERM
	select {
	case <-device.closed:
		t.Fatal("watcher closed the device after serving ended")
	default:
	}
}

func TestShutdownWatcherClosesDeviceOnSignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	device := &watchedDevice{closed: make(chan struct{})}
	stop := startShutdownWatcher(signals, device)
	defer stop()
	signals <- syscall.SIGTERM
	select {
	case <-device.closed:
	case <-time.After(time.Second):
		t.Fatal("watcher did not close the device on termination")
	}
}
