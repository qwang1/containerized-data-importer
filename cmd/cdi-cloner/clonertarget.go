package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/golang/glog"

	"kubevirt.io/containerized-data-importer/pkg/common"
	"kubevirt.io/containerized-data-importer/pkg/util"

	"github.com/prometheus/client_golang/prometheus"
)

type prometheusProgressReader struct {
	reader  io.Reader
	current int64
	total   int64
}

const (
	maxSizeLength = 20
)

var (
	progress = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "clone_progress",
			Help: "The clone progress in percentage",
		},
		[]string{"ownerUID"},
	)
	ownerUID  string
	namedPipe *string
)

func init() {
	namedPipe = flag.String("pipedir", "nopipedir", "The name and directory of the named pipe to read from")
	flag.Parse()
	prometheus.MustRegister(progress)
	ownerUID, _ = util.ParseEnvVar(common.OwnerUID, false)
}

func main() {
	defer glog.Flush()
	glog.V(1).Infoln("Starting cloner target")

	certsDirectory, err := ioutil.TempDir("", "certsdir")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(certsDirectory)
	util.StartPrometheusEndpoint(certsDirectory)

	if *namedPipe == "nopipedir" {
		glog.Errorf("%+v", fmt.Errorf("Missed named pipe flag"))
		os.Exit(1)
	}

	total, err := collectTotalSize()
	if err != nil {
		glog.Errorf("%+v", err)
		os.Exit(1)
	}

	//re-open pipe with fresh start.
	out, err := os.OpenFile(*namedPipe, os.O_RDONLY, 0600)
	if err != nil {
		glog.Errorf("%+v", err)
		os.Exit(1)
	}
	defer out.Close()

	promReader := &prometheusProgressReader{
		reader:  out,
		current: 0,
		total:   total,
	}

	// Start the progress update thread.
	go promReader.timedUpdateProgress()

	err = untar(promReader, ".")
	if err != nil {
		glog.Errorf("%+v", err)
		os.Exit(1)
	}

	glog.V(1).Infoln("clone complete")
}

func collectTotalSize() (int64, error) {
	glog.V(3).Infoln("Reading total size")
	out, err := os.OpenFile(*namedPipe, os.O_RDONLY, 0600)
	if err != nil {
		return int64(-1), err
	}
	defer out.Close()
	return readTotal(out), nil
}

func (r *prometheusProgressReader) timedUpdateProgress() {
	for true {
		// Update every second.
		time.Sleep(time.Second)
		r.updateProgress()
	}
}

func (r *prometheusProgressReader) updateProgress() {
	if r.total > 0 {
		currentProgress := float64(r.current) / float64(r.total) * 100.0
		progress.WithLabelValues(ownerUID).Set(currentProgress)
		glog.V(1).Infoln(fmt.Sprintf("%.2f", currentProgress))
	} else {
		progress.WithLabelValues(ownerUID).Set(-1)
	}
}

// Untar the contents of the passed in Reader.
func untar(r io.Reader, targetDir string) error {
	var buf bytes.Buffer
	untar := exec.Command("/usr/bin/tar", "xvC", targetDir)
	untar.Stdin = r
	untar.Stderr = &buf
	err := untar.Start()
	if err != nil {
		return err
	}
	err = untar.Wait()
	if err != nil {
		glog.Errorf("%s\n", string(buf.Bytes()))
		return err
	}
	return err
}

// Read reads bytes from the stream and updates the prometheus clone_progress metric according to the progress.
func (r *prometheusProgressReader) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	r.current += int64(n)
	return n, err
}

// read total file size from reader, and return the value as an int64
func readTotal(r io.Reader) int64 {
	totalScanner := bufio.NewScanner(r)
	if !totalScanner.Scan() {
		glog.Errorf("Unable to determine length of file")
		return -1
	}
	totalText := totalScanner.Text()
	total, err := strconv.ParseInt(totalText, 10, 64)
	if err != nil {
		glog.Errorf("%+v", err)
		return -1
	}
	glog.V(1).Infoln(fmt.Sprintf("total size: %s", totalText))
	return total
}
