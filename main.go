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
	"os"
	"os/signal"
	"syscall"

	"github.com/peios/loregd/internal/config"
	"github.com/peios/loregd/internal/device"
	"github.com/peios/loregd/internal/handler"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

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
		log.Printf("hive %s: root=%x maxseq=%d", h.Name, h.RootGUID, seq)
	}
	log.Printf("global max sequence: %d", globalMaxSeq)

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
	log.Printf("loregd: registered %d hive(s); entering request loop", len(hives))

	// peinit termination (PSD-006 §2 exit behaviour): close the device to
	// unblock the read loop, then run deferred cleanup.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("loregd: received %s, shutting down", sig)
		dev.Close()
	}()

	// Step 10: enter the request loop. Returns nil on device close
	// (LCS shutdown), or an error on malformed framing / I/O failure.
	if err := device.Serve(dev, disp); err != nil {
		return fmt.Errorf("request loop: %w", err)
	}
	log.Printf("loregd: device closed, exiting")
	return nil
}
