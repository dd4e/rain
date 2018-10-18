package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"

	"github.com/cenkalti/log"
	"github.com/cenkalti/rain/client"
	"github.com/cenkalti/rain/internal/clientversion"
	"github.com/cenkalti/rain/internal/logger"
	"github.com/cenkalti/rain/resume/torrentresume"
	"github.com/cenkalti/rain/storage/filestorage"
	"github.com/cenkalti/rain/torrent"
	"github.com/mitchellh/go-homedir"
	"github.com/urfave/cli"
)

var (
	cfg = client.NewConfig()
	app = cli.NewApp()
)

func main() {
	app.Version = clientversion.Version
	app.Usage = "BitTorrent client"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "config, c",
			Usage: "read config from `FILE`",
		},
		cli.StringFlag{
			Name:  "cpuprofile",
			Usage: "write cpu profile to `FILE`",
		},
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "enable debug log",
		},
		cli.StringFlag{
			Name:  "logfile",
			Usage: "write log to `FILE`",
		},
	}
	app.Before = handleBeforeCommand
	app.After = handleAfterCommand
	app.Commands = []cli.Command{
		{
			Name:      "download",
			Usage:     "download torrent or magnet",
			ArgsUsage: "[torrent path or magnet link]",
			Action:    handleDownload,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "dest",
					Usage: "save files under `DIR`",
					Value: ".",
				},
				cli.IntFlag{
					Name:  "port",
					Usage: "peer listen port",
				},
				cli.BoolFlag{
					Name:  "seed",
					Usage: "continue seeding after download finishes",
				},
			},
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func handleBeforeCommand(c *cli.Context) error {
	cpuprofile := c.GlobalString("cpuprofile")
	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
	}
	configPath := c.GlobalString("config")
	if configPath != "" {
		cp, err := homedir.Expand(configPath)
		if err != nil {
			log.Fatal(err)
		}
		err = cfg.LoadFile(cp)
		if err != nil {
			log.Fatal(err)
		}
	}
	logFile := c.GlobalString("logfile")
	if logFile != "" {
		f, err := os.Create(logFile)
		if err != nil {
			log.Fatal("could not create log file: ", err)
		}
		logger.Handler = log.NewFileHandler(f)
	}
	if c.GlobalBool("debug") {
		logger.Handler.SetLevel(log.DEBUG)
	}
	return nil
}

func handleAfterCommand(c *cli.Context) error {
	if c.GlobalString("cpuprofile") != "" {
		pprof.StopCPUProfile()
	}
	return nil
}

func handleDownload(c *cli.Context) error {
	path := c.Args().Get(0)
	if path == "" {
		return errors.New("first argument must be a torrent file or magnet link")
	}
	sto, err := filestorage.New(c.String("dest"))
	if err != nil {
		log.Fatal(err)
	}
	var t *torrent.Torrent
	if strings.HasPrefix(path, "magnet:") {
		t, err = torrent.NewMagnet(path, c.Int("port"), sto)
	} else {
		f, err2 := os.Open(path) // nolint: gosec
		if err2 != nil {
			log.Fatal(err2)
		}
		t, err = torrent.New(f, c.Int("port"), sto)
		_ = f.Close()
	}
	if err != nil {
		log.Fatal(err)
	}
	defer t.Close()

	res, err := torrentresume.New(t.Name() + "." + t.InfoHash() + ".resume")
	if err != nil {
		log.Fatal(err)
	}
	err = t.SetResume(res)
	if err != nil {
		log.Fatal(err)
	}

	go printStats(t)
	t.Start()

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-t.NotifyComplete():
			if !c.Bool("seed") {
				t.Stop()
				continue
			}
		case <-sigC:
			t.Stop()
		case err = <-t.NotifyError():
			return err
		}
	}
}

func printStats(t *torrent.Torrent) {
	for range time.Tick(100 * time.Millisecond) {
		b, err2 := json.MarshalIndent(t.Stats(), "", "  ")
		if err2 != nil {
			log.Fatal(err2)
		}
		fmt.Println(string(b))
	}
}
