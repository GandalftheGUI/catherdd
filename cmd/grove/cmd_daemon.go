package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func cmdDaemon() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: grove daemon <install|uninstall|status|logs>")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "install":
		cmdDaemonInstall()
	case "uninstall":
		cmdDaemonUninstall()
	case "status":
		cmdDaemonStatus()
	case "logs":
		cmdDaemonLogs()
	default:
		fmt.Fprintf(os.Stderr, "grove: unknown daemon subcommand %q\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdDaemonLogs() {
	fs := flag.NewFlagSet("daemon logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow log output")
	fs.BoolVar(follow, "follow", false, "follow log output")
	tailLines := fs.Int("n", 0, "print only the last N lines (0 = full file)")
	fs.IntVar(tailLines, "tail", 0, "print only the last N lines (0 = full file)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grove daemon logs [-f] [-n N]")
	}
	fs.Parse(os.Args[3:])
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: grove daemon logs [-f] [-n N]")
		os.Exit(1)
	}
	if *tailLines < 0 {
		fmt.Fprintln(os.Stderr, "grove: -n/--tail must be >= 0")
		os.Exit(1)
	}

	logPath := filepath.Join(rootDir(), "daemon.log")
	var err error
	if *tailLines > 0 {
		err = printLastLines(logPath, *tailLines, os.Stdout)
	} else {
		err = copyFileToStdout(logPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "grove: %v\n", err)
		os.Exit(1)
	}

	if *follow {
		if err := followFile(logPath); err != nil {
			fmt.Fprintf(os.Stderr, "grove: %v\n", err)
			os.Exit(1)
		}
	}
}

func copyFileToStdout(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("daemon log not found at %s", path)
		}
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(os.Stdout, f); err != nil {
		return fmt.Errorf("read daemon log: %w", err)
	}
	return nil
}

func printLastLines(path string, n int, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("daemon log not found at %s", path)
		}
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	ring := make([]string, n)
	count := 0
	for scanner.Scan() {
		ring[count%n] = scanner.Text()
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read daemon log: %w", err)
	}

	start := 0
	lines := count
	if count > n {
		start = count % n
		lines = n
	}
	for i := 0; i < lines; i++ {
		fmt.Fprintln(w, ring[(start+i)%n])
	}
	return nil
}

func followFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("daemon log not found at %s", path)
		}
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer f.Close()

	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek daemon log: %w", err)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
			info, err := f.Stat()
			if err != nil {
				return fmt.Errorf("stat daemon log: %w", err)
			}

			size := info.Size()
			if size < offset {
				offset = 0
			}
			if size <= offset {
				continue
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return fmt.Errorf("seek daemon log: %w", err)
			}
			if _, err := io.CopyN(os.Stdout, f, size-offset); err != nil && err != io.EOF {
				return fmt.Errorf("read daemon log: %w", err)
			}
			offset = size
		}
	}
}
