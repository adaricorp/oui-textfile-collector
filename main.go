package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
)

const (
	binName = "oui_textfile_collector"
	url     = "https://standards-oui.ieee.org/oui/oui.csv"
)

var (
	logLevel  *string
	slogLevel *slog.LevelVar = new(slog.LevelVar)

	refreshInterval *string
	metricFile      *string
	metricName      *string

	userAgent = binName + "/" + version.Version
)

// Print program usage
func printUsage(fs ff.Flags) {
	fmt.Fprintf(os.Stderr, "%s\n", ffhelp.Flags(fs))
	os.Exit(1)
}

// Print program version
func printVersion() {
	fmt.Printf("%s v%s built on %s\n", binName, version.Version, version.BuildDate)
	os.Exit(0)
}

func init() {
	fs := ff.NewFlagSet(binName)
	displayVersion := fs.BoolLong("version", "Print version")
	logLevel = fs.StringEnumLong(
		"log-level",
		"Log level: debug, info, warn, error",
		"info",
		"debug",
		"error",
		"warn",
	)
	refreshInterval = fs.StringLong(
		"refresh-interval",
		"168h",
		`Interval at which to refresh the OUI database. Valid time units are "ns", "us", "ms", "s", "m", "h"`,
	)
	metricFile = fs.StringLong(
		"output-file",
		"/var/lib/node_exporter/textfile/oui.prom",
		"Path to the file where metrics should be written",
	)
	metricName = fs.StringLong(
		"metric-name",
		"mac_oui_info",
		"Prometheus metric name",
	)

	err := ff.Parse(fs, os.Args[1:],
		ff.WithEnvVarPrefix(strings.ToUpper(binName)),
		ff.WithEnvVarSplit(" "),
	)
	if err != nil {
		printUsage(fs)
	}

	if *displayVersion {
		printVersion()
	}

	switch *logLevel {
	case "debug":
		slogLevel.Set(slog.LevelDebug)
	case "info":
		slogLevel.Set(slog.LevelInfo)
	case "warn":
		slogLevel.Set(slog.LevelWarn)
	case "error":
		slogLevel.Set(slog.LevelError)
	}

	logger := slog.New(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slogLevel,
		}),
	)
	slog.SetDefault(logger)
}

func update() (string, error) {
	f, err := os.CreateTemp("", "oui.csv")
	if err != nil {
		return "", errors.Wrapf(err, "Error creating temporary file")
	}
	defer f.Close()

	filename := f.Name()

	client := http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return filename, errors.Wrapf(err, "Error creating http request")
	}

	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return filename, errors.Wrapf(err, "Error doing http request")
	}
	defer resp.Body.Close()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return filename, errors.Wrapf(err, "Error writing to temporary file")
	}

	return filename, nil
}

func parse(filename string) error {
	input, err := os.Open(filename)
	if err != nil {
		return errors.Wrapf(err, "Error opening OUI CSV file")
	}
	defer input.Close()

	output, err := os.Create(*metricFile + ".tmp")
	if err != nil {
		return errors.Wrapf(err, "Error opening temporary OUI metric file")
	}
	defer output.Close()

	ouiMap := map[string]string{}

	first := true
	csv := csv.NewReader(input)

	for {
		entry, err := csv.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			return errors.Wrapf(err, "Error parsing OUI CSV file")
		}

		if first {
			// Skip CSV header
			first = false

			continue
		}

		oui := strings.ToLower(entry[1])
		organization := strings.TrimSpace(entry[2])

		if len(oui) != 6 {
			slog.Error("OUI has wrong number of characters", "oui", oui)

			continue
		}

		oui = oui[0:2] + ":" + oui[2:4] + ":" + oui[4:6]

		if cur, exists := ouiMap[oui]; exists {
			// Merge organization names if multiple exist for same OUI
			ouiMap[oui] = strings.Join([]string{cur, organization}, " | ")
		} else {
			ouiMap[oui] = organization
		}
	}

	for oui, organization := range ouiMap {
		_, err := output.WriteString(
			fmt.Sprintf(
				`%s{oui="%s",organization_name="%s"} 1`,
				*metricName,
				strings.ReplaceAll(oui, `"`, `\"`),
				strings.ReplaceAll(organization, `"`, `\"`),
			) + "\n",
		)
		if err != nil {
			return errors.Wrapf(err, "Error writing to temporary OUI metric file")
		}
	}

	if err := os.Rename(*metricFile+".tmp", *metricFile); err != nil {
		return errors.Wrapf(err, "Error renaming OUI metric file")
	}

	return nil
}

// Calculate how many seconds to backoff for a given retry attempt
func backoff(retries int) time.Duration {
	expo := int(math.Pow(2, float64(retries+2)))

	half := int(expo / 2)

	random := 0
	if half >= 1 {
		random = rand.Intn(half)
	}

	// Cap maximum backoff time at 1 day
	return min(
		(time.Duration(expo+random) * time.Second),
		(24 * time.Hour),
	)
}

func main() {
	slog.Info(
		fmt.Sprintf("Starting %s", binName),
		"version",
		version.Version,
		"build_context",
		fmt.Sprintf(
			"go=%s, platform=%s",
			runtime.Version(),
			runtime.GOOS+"/"+runtime.GOARCH,
		),
	)

	timerDuration, err := time.ParseDuration(*refreshInterval)
	if err != nil {
		slog.Error("Error parsing refresh interval", "interval", *refreshInterval)
		os.Exit(1)
	}

	timer := time.NewTimer(time.Until(time.Now()))
	defer timer.Stop()

	retries := 0

	for {
		<-timer.C
		slog.Info("Updating OUI database")

		filename, err := update()
		if err != nil {
			slog.Error(
				"Error updating OUI database",
				"error",
				err.Error(),
				"retry",
				backoff(retries),
			)

			retries++
			timer.Reset(backoff(retries))

			continue
		}

		if err := parse(filename); err != nil {
			slog.Error(
				"Error parsing OUI database",
				"error",
				err.Error(),
				"retry",
				backoff(retries),
			)

			retries++
			timer.Reset(backoff(retries))

			continue
		}

		if err := os.Remove(filename); err != nil {
			slog.Error("Error removing temporary file", "error", err.Error())
		}

		retries = 0

		slog.Info("Successfully updated OUI database")

		slog.Info("Next OUI database refresh time", "time", time.Now().Add(timerDuration))

		timer.Reset(timerDuration)
	}
}
