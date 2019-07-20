package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ccassar/raft"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func (a *app) run(sigChan chan os.Signal, lcfg zap.Config) {

	err := a.configure(lcfg)
	if err != nil {
		os.Exit(-1)
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-sigChan:
			// any of the signals result in exit.
			a.lg.Info("application received shutdown signal")
			cancel()
		}
	}()

	wg.Add(1)
	node, err := raft.MakeNode(ctx, &wg, a.nc, int32(a.localNode), a.opts...)
	if err != nil {
		a.lg.Errorf("application failed to create Raft node: %v", err)
		os.Exit(-1)
	}

	count := 1
	wg.Add(1)
	go func() {
		defer wg.Done()

		a.lg.Info("Application loop started: ", a.localNode)

		produceTrigger := time.After(a.nextLogCmdPeriod())
		for {
			select {
			case <-ctx.Done():
				return
			case err = <-node.FatalErrorChannel():
				a.lg.Errorw("fatal error from Raft", "err", err)
				return
			case <-produceTrigger:
				ctxWithTimeout, cancelMsg := context.WithTimeout(ctx, a.logCmdTimeout)
				msg := a.nextLogCmd()
				a.lg.Infow("logCmd Txed", "cmd", string(msg))
				err = node.LogProduce(ctxWithTimeout, msg)
				if err != nil {
					a.lg.Infow("logCmd Txed, failed", "cmd", string(msg), "err", err)
				}
				cancelMsg()
				// rearm the log producer
				produceTrigger = time.After(a.nextLogCmdPeriod())
			case msg := <-a.nc.LogCmds:
				// log and count. Dump message to stdout.
				a.lg.Infow("logCmd Rxed", "cmd", string(msg), "order", count)
				fmt.Println(string(msg))
				count++
			}
		}

	}()

	wg.Wait()
}

// appCfg is the recipient of the JSON configuration.
type appCfg struct {
	// Nodes in cluster as required and documented in raft package...
	Nodes []string
	// LogDB provides the location of the bbolt db file.
	LogDB string
	// Configure the period with which we publish the random <origin ID: UUID> log command.
	LogCmdPeriod string
	// Configure the timeout we wait for publishing success before cancellation.
	LogCmdTimeout string
	// Configure raft leader timeout.
	LeaderTimeout string
	// Set up metrics export.
	Metrics struct {
		// e.g. localhost:9000
		Endpoint string
		// e.g. /metrics
		Path string
		// e.g. myAppNamespace
		Namespace string
	}
}

type app struct {
	// logCmd production control; a period (which is jittered) and a configurable timeout.
	logCmdPeriod  time.Duration
	logCmdTimeout time.Duration
	// Prepared node configuration.
	nc raft.NodeConfig
	// Prepare options.
	opts []raft.NodeOption
	// Configuration file and local node index (i.e. identity in cluster, 0..n-1 for a cluster of n).
	cfgFile   string
	localNode int
	// logging (as in zap logging, not the distributed log) configuration.
	lg          *zap.SugaredLogger
	debug       bool
	zapFile     string
	zapEncoding string
}

func (a *app) nextLogCmdPeriod() time.Duration {
	jittered := int(a.logCmdPeriod)/2 + rand.Intn(int(a.logCmdPeriod))
	return time.Duration(jittered)
}

func (a *app) nextLogCmd() []byte {
	return []byte(fmt.Sprintf("Node%d:%s", a.localNode, uuid.New().String()))
}

// configure processes configuration file to build NodeConfig and subset of options we support in the example app.
func (a *app) configure(lcfg zap.Config) error {

	if a.debug {
		lcfg.Level.SetLevel(zapcore.DebugLevel)
	}

	if a.zapEncoding != "" {
		lcfg.Encoding = a.zapEncoding
	}

	if a.zapFile != "" {
		lcfg.OutputPaths = []string{a.zapFile}
	}

	lcfg.DisableStacktrace = true
	lg, err := lcfg.Build()
	if err != nil {
		fmt.Println("Failed to start app with logger configuration failure", err)
	}
	a.lg = lg.Sugar()

	//
	// Next, let's load configuration file.
	fstream, err := ioutil.ReadFile(a.cfgFile)
	if err != nil {
		a.lg.Errorf("Failed to load configuration file [%v]", err)
		return err
	}

	var ac appCfg
	err = json.Unmarshal(fstream, &ac)
	if err != nil {
		a.lg.Errorf("Failed to unmarshal configuration file [%v]", err)
		return err
	}

	a.nc = raft.NodeConfig{
		Nodes:   ac.Nodes,
		LogDB:   fmt.Sprintf("%s%d", ac.LogDB, a.localNode),
		LogCmds: make(chan []byte, 32),
	}

	a.opts = []raft.NodeOption{
		raft.WithLogger(lg, false)}

	if ac.LogCmdPeriod != "" {
		a.logCmdPeriod, err = time.ParseDuration(ac.LogCmdPeriod)
		if err != nil {
			a.lg.Errorf("Failed to parse LogCmdPeriod [%v]", err)
			return err
		}
	} else {
		a.logCmdPeriod = time.Second * 30 // jittered default
	}

	if ac.LogCmdTimeout != "" {
		a.logCmdTimeout, err = time.ParseDuration(ac.LogCmdTimeout)
		if err != nil {
			a.lg.Errorf("Failed to parse LogCmdTimeout [%v]", err)
			return err
		}
	} else {
		a.logCmdTimeout = time.Second * 5
	}

	if ac.LeaderTimeout != "" {
		electionTimeout, err := time.ParseDuration(ac.LeaderTimeout)
		if err != nil {
			a.lg.Errorf("Failed to parse election timeout '%s' [%v]", ac.LeaderTimeout, err)
			return err
		}
		a.opts = append(a.opts, raft.WithLeaderTimeout(electionTimeout))
	}

	if ac.Metrics.Endpoint != "" {

		metricsReg := prometheus.NewRegistry()
		handler := promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{})

		handlerMux := http.NewServeMux()
		handlerMux.Handle(ac.Metrics.Path, handler)
		metricServer := &http.Server{
			Addr:    ac.Metrics.Endpoint,
			Handler: handlerMux,
		}

		go func() {
			err := metricServer.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				a.lg.Errorf("Failed to serve metrics for application, cfg: '%s' [%+v]", ac.Metrics, err)
			}
		}()

		a.opts = append(a.opts, raft.WithMetrics(metricsReg, ac.Metrics.Namespace, true))
	}

	return err
}

func main() {

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGABRT)

	// Seed random generation here (test functions enter beneath here and allow tests to
	// determine whether to use a consistent seed or random seeds).
	rand.Seed(time.Now().UnixNano())

	var a app

	flag.BoolVar(&a.debug, "debug", false, "enable debug")
	flag.StringVar(&a.cfgFile, "config", "app.json", "specify a configuration filename")
	flag.StringVar(&a.zapEncoding, "zapEncoding", "console", "specify application zap log encoding")
	flag.StringVar(&a.zapFile, "zapFile", "", "specify application zap log file (log to stderr if not set)")
	flag.IntVar(&a.localNode, "localNode", 0, "specify the localNode index")
	flag.Parse()

	a.run(sigChan, raft.DefaultZapLoggerConfig())
}
