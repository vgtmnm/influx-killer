/*
   Copyright 2017 Odd Eivind Ebbesen

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package main

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/influxdata/influxdb/client/v2"
	"github.com/urfave/cli" // renamed from codegansta
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	VERSION        string  = "2016-09-07"
	DEF_DB         string  = "custom"
	DEF_HOSTPREFIX string  = "hetsfan"
	DEF_TIMEOUT    float64 = 66.6
	DEF_W_TIMEOUT  float64 = 5.0
	DEF_INTERVAL   float64 = 1.3
	DEF_POINTS     uint    = 256
	DEF_NUMHOSTS   uint    = 64
)

type Worker struct {
	Client    client.Client
	Hostname  string
	DB        string
	NumPoints int
	Interval  time.Duration
	Done      chan bool
	Cancel    chan bool
}

var regions = [...]string{
	"eu-west-1",
	"eu-west-2",
	"us-east-1",
	"us-east-2",
}

func (w *Worker) Work() {
	for {
		select {
		case <-w.Cancel:
			log.WithFields(log.Fields{
				"worker": w.Hostname,
			}).Debug("Quitting...")
			err := w.Client.Close()
			if err != nil {
				log.WithFields(log.Fields{
					"worker": w.Hostname,
					"error":  err,
				}).Error("Client close")
			}
			w.Done <- true
			return
		default:
			// carry on
		}
		log.WithFields(log.Fields{
			"worker":     w.Hostname,
			"num_points": w.NumPoints,
		}).Debug("Writing...")
		err := w.Write()
		if err != nil {
			log.WithFields(log.Fields{
				"worker": w.Hostname,
				"error":  err,
			}).Error("Client write")
		}
		log.WithFields(log.Fields{
			"worker":   w.Hostname,
			"interval": w.Interval,
		}).Debug("Sleeping...")
		time.Sleep(w.Interval)
	}
}

// inspired (almost copied) by https://github.com/influxdata/influxdb/blob/master/client/README.md
func (w *Worker) Write() error {
	bp, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database:  w.DB,
		Precision: "ms",
	})
	if err != nil {
		log.WithFields(log.Fields{
			"worker": w.Hostname,
			"error":  err,
		}).Error("Create batch points")
		return err
	}
	max := 100.0
	for i := 0; i < w.NumPoints; i++ {
		tags := map[string]string{
			"cpu":    "cpu-total",
			"host":   w.Hostname,
			"region": regions[rand.Intn(len(regions))],
		}
		idle := rand.Float64() * max
		fields := map[string]interface{}{
			"idle": idle,
			"busy": max - idle,
		}
		p, err := client.NewPoint("cpu_usage", tags, fields, time.Now())
		if err != nil {
			log.WithFields(log.Fields{
				"worker": w.Hostname,
				"error":  err,
			}).Error("Create point")
			return err
		}
		bp.AddPoint(p)
	}
	return w.Client.Write(bp)
}

func NewWorker(hostname, db, addr string, numpoints int, interval, timeout float64, cancel, done chan bool) *Worker {
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:    addr,
		Timeout: time.Duration(timeout*1000) * time.Millisecond,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"worker": hostname,
			"error":  err,
		}).Error("Create HTTP client")
		return nil
	}
	return &Worker{
		Client:    c,
		Hostname:  hostname,
		DB:        db,
		NumPoints: numpoints,
		Interval:  time.Duration(interval*1000) * time.Millisecond,
		Cancel:    cancel,
		Done:      done,
	}
}

func startStress(c *cli.Context) error {
	nw := c.Int("num-hosts")
	np := c.Int("num-points")
	iv := c.Float64("interval")
	to := c.Float64("timeout")
	hp := c.String("host-prefix")
	db := c.String("db")
	url := c.String("url")
	wto := c.Float64("write-timeout")

	if url == "" {
		return cli.NewExitError("You must specify a URL", 1)
	}
	if db == "" {
		return cli.NewExitError("You must specify a database", 2)
	}

	done := make(chan bool)
	cancel := make(chan bool, nw)
	sig := make(chan os.Signal, 1)

	cancel_workers := func() {
		for i := 0; i < nw; i++ {
			cancel <- true
		}
	}

	await_workers := func() {
		for i := 0; i < nw; i++ {
			<-done
		}
	}

	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.WithFields(log.Fields{
			"signal": s,
		}).Debug("Exiting from signal")
		cancel_workers()
	}()

	for i := 0; i < nw; i++ {
		w := NewWorker(fmt.Sprintf("%s-%05d", hp, i), db, url, np, iv, wto, cancel, done)
		if w != nil {
			go func() {
				// randomize the start of each worker with a delay of 0.0 - 1.0 sec
				time.Sleep(time.Millisecond * time.Duration(rand.Float64()*1000))
				w.Work()
			}()
		}
	}

	select {
	case <-time.After(time.Second * time.Duration(to)):
		cancel_workers()
	}

	await_workers()

	return nil
}

func main() {
	app := cli.NewApp()
	app.Name = "influx-killer"
	app.Version = VERSION
	app.Authors = []cli.Author{
		cli.Author{
			Name:  "Odd E. Ebbesen",
			Email: "odd.ebbesen@wirelesscar.com",
		},
	}
	app.Usage = "Stresstest InfluxDB"

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "url, u",
			Usage: "Full URL (with port) to Influx endpoint",
		},
		cli.StringFlag{
			Name:  "db",
			Usage: "Name of database to write to",
		},
		cli.StringFlag{
			Name:  "host-prefix",
			Usage: "Prefix for generated hostnames",
			Value: DEF_HOSTPREFIX,
		},
		cli.UintFlag{
			Name:  "num-hosts, n",
			Usage: "Number of hosts to simulate traffic from",
			Value: DEF_NUMHOSTS,
		},
		cli.Float64Flag{
			Name:  "interval, i",
			Usage: "How long (in seconds, fractions allowed) between sending metrics",
			Value: DEF_INTERVAL,
		},
		cli.Float64Flag{
			Name:  "timeout, t",
			Usage: "How long in seconds (fractions allowed) to run the test",
			Value: DEF_TIMEOUT,
		},
		cli.Float64Flag{
			Name:  "write-timeout, w",
			Usage: "Timeout for each write operation",
			Value: DEF_W_TIMEOUT,
		},
		cli.UintFlag{
			Name:  "num-points, p",
			Usage: "Number of points per batch",
			Value: DEF_POINTS,
		},
		cli.StringFlag{
			Name:  "log-level, l",
			Value: "error",
			Usage: "Log level (options: debug, info, warn, error, fatal, panic)",
		},
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "Run in debug mode",
		},
	}

	app.Before = func(c *cli.Context) error {
		rand.Seed(time.Now().UnixNano())
		//log.SetOutput(os.Stderr)
		level, err := log.ParseLevel(c.String("log-level"))
		if err != nil {
			log.Fatal(err.Error())
		}
		log.SetLevel(level)
		if !c.IsSet("log-level") && !c.IsSet("l") && c.Bool("debug") {
			log.SetLevel(log.DebugLevel)
		}
		log.SetFormatter(&log.TextFormatter{
			DisableTimestamp: false,
			FullTimestamp:    true,
		})
		return nil
	}
	app.Action = startStress
	app.Run(os.Args)
}
