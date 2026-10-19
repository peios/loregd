// loregd is the Local Registry Daemon for Peios.
//
// loregd implements the Registry Source Interface (RSI) defined in
// the LCS v0.21 specification, providing persistent storage for
// one or more registry hives using SQLite.
//
// Usage:
//
//	loregd HiveName=DatabasePath [HiveName=DatabasePath ...]
package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/peios/loregd/internal/config"
	"github.com/peios/loregd/internal/device"
	"github.com/peios/loregd/internal/handler"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// infoLog sends progress to stdout. peinit and eventd reserve stderr for faults;
// neither stream should bypass their capture pipes by writing to /dev/console.
var infoLog = log.New(os.Stdout, "", log.LstdFlags)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	configs, err := config.Parse(args)
	if err != nil {
		return fmt.Errorf("argument error: %w", err)
	}

	runtimeStore, err := hivedb.OpenRuntimeStore("/run/loregd")
	if err != nil {
		return fmt.Errorf("volatile storage: %w", err)
	}
	defer closeResource("runtime store", runtimeStore)

	hives, registrations, maxSequence, err := openHives(configs, runtimeStore.Path)
	defer closeHives(hives)
	if err != nil {
		return err
	}

	// Wire RSI operation handlers onto a dispatcher.
	disp := rsi.NewDispatcher()
	handlers := handler.New(hives)
	defer handlers.Close()
	handlers.Register(disp)

	// Step 8: open /dev/pkm_registry (kernel checks SeTcbPrivilege).
	dev, err := device.Open()
	if err != nil {
		return err
	}
	defer closeResource("registry device", dev)
	lifetime, err := device.Negotiate(dev)
	if err != nil {
		return err
	}
	handlers.EnableLeases(lifetime.Renew)

	if err := registerHives(dev, registrations, maxSequence); err != nil {
		return err
	}
	infoLog.Printf("loregd: registered %d hive(s); entering request loop", len(hives))

	// Signal readiness to the supervisor (peinit's Phase-1 registryd bootstrap
	// waits for sd_notify READY=1 on $NOTIFY_SOCKET before declaring registryd
	// up). Done now: the hives are registered with the kernel and we are about
	// to serve requests, so the registry is usable.
	notifyReady()

	// peinit termination (PSD-006 §2 exit behaviour): close the device to
	// unblock the read loop, then run deferred cleanup.
	stopWatcher := watchShutdown(dev)
	defer stopWatcher()

	// Step 10: enter the request loop. Returns nil on device close
	// (LCS shutdown), or an error on malformed framing / I/O failure.
	if err := device.ServeWithLifetime(dev, disp, lifetime); err != nil {
		return fmt.Errorf("request loop: %w", err)
	}
	infoLog.Printf("loregd: device closed, exiting")
	return nil
}

// openHives returns every opened hive even on failure; the caller owns cleanup.
func openHives(configs []config.HiveConfig, volatileRoot string) (
	hives []*hivedb.HiveDB, registrations []device.HiveRegistration, maxSequence uint64, err error,
) {
	hives = make([]*hivedb.HiveDB, 0, len(configs))
	// Startup steps 1-7 (PSD-006 §2): parse, open, WAL, volatile attach,
	// schema, first-boot root creation, crash recovery, max sequence.
	registrations = make([]device.HiveRegistration, 0, len(configs))
	for _, cfg := range configs {
		hive, err := hivedb.OpenIn(cfg.Name, cfg.Path, volatileRoot)
		if err != nil {
			return hives, nil, 0, fmt.Errorf("hive %s: %w", cfg.Name, err)
		}
		hives = append(hives, hive)

		seq, err := hive.MaxSequence()
		if err != nil {
			return hives, nil, 0, fmt.Errorf("hive %s: %w", cfg.Name, err)
		}
		if seq > maxSequence {
			maxSequence = seq
		}

		registrations = append(registrations, device.HiveRegistration{
			Name: hive.Name, RootGUID: hive.RootGUID,
		})
		infoLog.Printf("hive %s: root=%x maxseq=%d", hive.Name, hive.RootGUID, seq)
	}
	infoLog.Printf("global max sequence: %d", maxSequence)

	return hives, registrations, maxSequence, nil
}

func closeHives(hives []*hivedb.HiveDB) {
	for _, hive := range hives {
		closeResource("hive "+hive.Name, hive)
	}
}

// closeResource reports a cleanup failure without replacing the primary result.
// Device shutdown may already have closed a descriptor to interrupt the read loop.
func closeResource(name string, resource io.Closer) {
	if err := resource.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		log.Printf("loregd: close %s: %v", name, err)
	}
}

// registerHives uses Control because File.Fd switches the descriptor to blocking
// mode. Keeping it in the runtime poller lets Close interrupt Serve's read.
func registerHives(
	dev *os.File, registrations []device.HiveRegistration, maxSequence uint64,
) error {
	raw, err := dev.SyscallConn()
	if err != nil {
		return fmt.Errorf("register hives: %w", err)
	}
	var registerErr error
	if err := raw.Control(func(fd uintptr) {
		registerErr = device.Register(fd, registrations, maxSequence)
	}); err != nil {
		return fmt.Errorf("register hives: %w", err)
	}
	if registerErr != nil {
		return fmt.Errorf("register hives: %w", registerErr)
	}
	return nil
}

// watchShutdown owns the signal registration and joins its watcher on cleanup.
func watchShutdown(dev io.Closer) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	stopWatcher := startShutdownWatcher(signals, dev)
	return func() {
		signal.Stop(signals)
		stopWatcher()
	}
}

// startShutdownWatcher closes the device once on a signal, or exits without
// touching it when serving ends first. The caller must invoke its stop function.
func startShutdownWatcher(signals <-chan os.Signal, dev io.Closer) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-signals:
			infoLog.Printf("loregd: received %s, shutting down", sig)
			closeResource("registry device", dev)
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// Readiness is best-effort; a stalled supervisor must not prevent serving.
const readinessTimeout = time.Second

func notifyReady() {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	if err := sendReady(path, readinessTimeout); err != nil {
		log.Printf("loregd: NOTIFY_SOCKET %q: %v", path, err)
	}
}

func sendReady(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.Dial("unixgram", path)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	// Closing an unbuffered datagram socket cannot complete a failed notification.
	defer func() { _ = conn.Close() }()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := conn.Write([]byte("READY=1\n")); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	return nil
}
