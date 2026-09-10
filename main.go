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
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/peios/loregd/internal/config"
	"github.com/peios/loregd/internal/device"
	"github.com/peios/loregd/internal/handler"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// infoLog writes ordinary progress to stdout; the standard logger keeps stderr
// for faults.
//
// peinit gives a service one pipe per stream and eventd records `is_error`
// from which one a line arrived on. Go's standard logger writes to stderr, so
// with loregd's output finally reaching eventd (rather than being written past
// it onto the console), every ordinary startup line was being recorded as an
// error — four false errors in the audit trail on every good boot.
//
// Only unambiguous progress belongs here. Anything reporting a fault stays on
// the standard logger, which is most of internal/handler.
var infoLog = log.New(os.Stdout, "", log.LstdFlags)

func main() {
	// Logs go to stderr, which is where peinit put them.
	//
	// This used to open /dev/console and point the logger there, because
	// peinit gave registryd capture pipes and then never drained them in
	// Phase 1: a loregd that printed exactly why it could not serve was
	// reported to the operator as nothing but "registryd readiness timeout
	// expired before READY=1". Writing to the console fixed the silence and
	// cost more than it bought — the lines left the pipe, so they never
	// reached the pre-eventd buffer and eventd never received loregd's
	// startup output at all. They were also the only untagged lines in an
	// otherwise formatted boot log.
	//
	// peinit now relays whatever registryd printed when Phase 1 fails
	// (init/linux/registryd.rs, report_registryd_output), so the diagnostic
	// is back without the console write. On a good boot these lines go where
	// every other service's output goes: the pre-eventd buffer, then eventd,
	// where `evctl` can query them.
	//
	// Do not reinstate the console redirect without first checking that
	// peinit still relays. The two changes are ordered, and reversing only
	// this half restores the silent failure.
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	configs, err := config.Parse(args)
	if err != nil {
		return fmt.Errorf("argument error: %w", err)
	}

	hives := make([]*hivedb.HiveDB, 0, len(configs))
	defer func() {
		for _, h := range hives {
			h.Close()
		}
	}()

	// Startup steps 1-7 (PSD-006 §2): parse, open, WAL, volatile attach,
	// schema, first-boot root creation, crash recovery, max sequence.
	var globalMaxSeq uint64
	regs := make([]device.HiveRegistration, 0, len(configs))
	for _, cfg := range configs {
		h, err := hivedb.Open(cfg.Name, cfg.Path)
		if err != nil {
			return fmt.Errorf("hive %s: %w", cfg.Name, err)
		}
		hives = append(hives, h)

		seq, err := h.MaxSequence()
		if err != nil {
			return fmt.Errorf("hive %s: %w", cfg.Name, err)
		}
		if seq > globalMaxSeq {
			globalMaxSeq = seq
		}

		regs = append(regs, device.HiveRegistration{Name: h.Name, RootGUID: h.RootGUID})
		infoLog.Printf("hive %s: root=%x maxseq=%d", h.Name, h.RootGUID, seq)
	}
	infoLog.Printf("global max sequence: %d", globalMaxSeq)

	// Wire RSI operation handlers onto a dispatcher.
	disp := rsi.NewDispatcher()
	handler.New(hives).Register(disp)

	// Step 8: open /dev/pkm_registry (kernel checks SeTcbPrivilege).
	dev, err := device.Open()
	if err != nil {
		return err
	}
	defer dev.Close()

	// Step 9: register all hives, root GUIDs, and the global max sequence.
	if err := device.Register(dev.Fd(), regs, globalMaxSeq); err != nil {
		return fmt.Errorf("register hives: %w", err)
	}
	infoLog.Printf("loregd: registered %d hive(s); entering request loop", len(hives))

	// Signal readiness to the supervisor (peinit's Phase-1 registryd bootstrap
	// waits for sd_notify READY=1 on $NOTIFY_SOCKET before declaring registryd
	// up). Done now: the hives are registered with the kernel and we are about
	// to serve requests, so the registry is usable.
	notifyReady()

	// peinit termination (PSD-006 §2 exit behaviour): close the device to
	// unblock the read loop, then run deferred cleanup.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		infoLog.Printf("loregd: received %s, shutting down", sig)
		dev.Close()
	}()

	// Step 10: enter the request loop. Returns nil on device close
	// (LCS shutdown), or an error on malformed framing / I/O failure.
	if err := device.Serve(dev, disp); err != nil {
		return fmt.Errorf("request loop: %w", err)
	}
	infoLog.Printf("loregd: device closed, exiting")
	return nil
}

// notifyReady sends an sd_notify READY=1 datagram to $NOTIFY_SOCKET so the
// supervisor (peinit) learns loregd is serving. Best-effort: a no-op when
// NOTIFY_SOCKET is unset, and a logged warning (never fatal) on dial/write
// failure — readiness signalling must not block or fail the request loop.
func notifyReady() {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	conn, err := net.Dial("unixgram", path)
	if err != nil {
		log.Printf("loregd: NOTIFY_SOCKET %q dial failed: %v", path, err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("READY=1\n")); err != nil {
		log.Printf("loregd: NOTIFY_SOCKET %q write failed: %v", path, err)
	}
}
