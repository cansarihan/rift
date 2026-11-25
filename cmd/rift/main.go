// Command rift compares two builds of the same program and surfaces the
// functions whose change looks like a quietly shipped security fix.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/cansarihan/rift/internal/binfile"
	"github.com/cansarihan/rift/internal/diff"
	"github.com/cansarihan/rift/internal/logging"
	"github.com/cansarihan/rift/internal/report"
	"github.com/cansarihan/rift/internal/server"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "rift: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return usage()
	}
	switch os.Args[1] {
	case "diff":
		return runDiff(os.Args[2:])
	case "inspect":
		return runInspect(os.Args[2:])
	case "serve":
		return runServe(os.Args[2:])
	case "version":
		fmt.Printf("rift %s (commit %s, built %s)\n", version, commit, date)
		return nil
	default:
		return usage()
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, "usage: rift <diff|inspect|serve|version> [flags]")
	fmt.Fprintln(os.Stderr, "  rift diff old-binary new-binary")
	fmt.Fprintln(os.Stderr, "  rift inspect binary")
	fmt.Fprintln(os.Stderr, "  rift serve -old old-binary -new new-binary -listen 127.0.0.1:8080")
	return errors.New("no command given")
}

func runDiff(args []string) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON instead of a table")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("diff needs two binaries: rift diff old new")
	}

	oldImg, err := binfile.Open(flags.Arg(0))
	if err != nil {
		return err
	}
	newImg, err := binfile.Open(flags.Arg(1))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	summary, err := diff.Run(ctx, oldImg, newImg, nil)
	if err != nil {
		return err
	}

	if *asJSON {
		return report.JSON(os.Stdout, summary)
	}
	report.Table(os.Stdout, summary)
	return nil
}

func runInspect(args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON instead of a summary")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("inspect needs one binary: rift inspect binary")
	}

	img, err := binfile.Open(flags.Arg(0))
	if err != nil {
		return err
	}

	if *asJSON {
		type fn struct {
			Name string `json:"name"`
			Addr uint64 `json:"addr"`
			Size uint64 `json:"size"`
		}
		out := struct {
			Path   string `json:"path"`
			Format string `json:"format"`
			Arch   string `json:"arch"`
			Funcs  []fn   `json:"funcs"`
		}{Path: img.Path, Format: img.Format, Arch: string(img.Arch)}
		for _, f := range img.Funcs {
			out.Funcs = append(out.Funcs, fn{Name: f.Name, Addr: f.Addr, Size: f.Size})
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	fmt.Printf("%s\n%s %s  ·  %d functions\n", img.Path, img.Format, img.Arch, len(img.Funcs))
	return nil
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:8080", "listen address")
	oldPath := flags.String("old", "", "the earlier build to compare")
	newPath := flags.String("new", "", "the later build to compare")
	token := flags.String("token", os.Getenv("RIFT_TOKEN"), "admin token")
	noUI := flags.Bool("no-ui", false, "disable the panel")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *oldPath == "" || *newPath == "" {
		return errors.New("serve needs -old and -new binaries")
	}

	logger := logging.New("info", "text", os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler := server.New(server.Options{
		Old:   *oldPath,
		New:   *newPath,
		Token: *token,
		UI:    !*noUI,
	}, server.BuildInfo{
		Version:   version,
		Commit:    commit,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}).Handler()

	httpServer := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("rift listening", "address", *listen, "old", *oldPath, "new", *newPath, "ui", !*noUI)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdown)
}
