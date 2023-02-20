package main

import (
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	volume_stat_size_bytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_size_bytes",
		Help: "Size of all files in the path of the volume",
	})
	volume_stat_files = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_files",
		Help: "number of files in the directory",
	})
	volume_stat_directories = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_directories",
		Help: "number of directories in the directory",
	})
	volume_stat_errors = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_errors",
		Help: "number of errors while reading files",
	})
	volume_stat_runtime_seconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_runtime_seconds",
		Help: "stat time in microseconds",
	})
	volume_stat_blocks_available_bytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_available_bytes",
		Help: "available blocks on path",
	})
	volume_stat_blocks_free_bytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_free_byte",
		Help: "free blocks on path",
	})
	volume_stat_blocks_used_bytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_used_bytes",
		Help: "used blocks on path",
	})
	volume_stat_blocks_size_bytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_size_bytes",
		Help: "size of blocks on volume in path",
	})
)

func main() {

	port := flag.Int("port", 8080, "port to use")
	host := flag.String("host", "", "host to use")
	flag.Parse()

	var path = flag.Arg(0)
	if path == "" {
		panic("no path defined")
	}

	quit := make(chan struct{})
	ticker := time.NewTicker(60 * time.Second)

	UpdateInfo(path)

	go func() {
		for {
			select {
			case <-ticker.C:
				UpdateInfo(path)
			case <-quit:
				return
			}
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(200)
	})
	fmt.Printf("Listening on... %s:%d\n", *host, *port)
	err := http.ListenAndServe(fmt.Sprintf("%s:%d", *host, *port), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	ticker.Stop()
	quit <- struct{}{}
}

func UpdateInfo(path string) {
	fmt.Println("updating stats...")
	var size int64 = 0
	var files int64 = 0
	var folders int64 = 0
	var errors int64 = 0
	var runtime int64 = time.Now().UnixMicro()

	filepath.Walk(path, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			errors = errors + 1
		} else {
			size = info.Size() + size
			if info.IsDir() {
				folders = folders + 1
			} else {
				files = files + 1
			}
		}
		return nil
	})
	usage := NewDiskUsage(path)
	runtime = time.Now().UnixMicro() - runtime

	volume_stat_runtime_seconds.Set(float64(runtime / 1000000))
	volume_stat_size_bytes.Set(float64(size))
	volume_stat_files.Set(float64(files))
	volume_stat_directories.Set(float64(folders))
	volume_stat_errors.Set(float64(errors))
	volume_stat_blocks_available_bytes.Set(float64(usage.Available()))
	volume_stat_blocks_free_bytes.Set(float64(usage.Free()))
	volume_stat_blocks_used_bytes.Set(float64(usage.Used()))
	volume_stat_blocks_size_bytes.Set(float64(usage.Size()))
}

// DiskUsage contains usage data and provides user-friendly access methods
type DiskUsage struct {
	stat *syscall.Statfs_t
}

// NewDiskUsages returns an object holding the disk usage of volumePath
// or nil in case of error (invalid path, etc)
func NewDiskUsage(volumePath string) *DiskUsage {

	var stat syscall.Statfs_t
	syscall.Statfs(volumePath, &stat)
	return &DiskUsage{&stat}
}

// Free returns total free bytes on file system
func (du *DiskUsage) Free() uint64 {
	return du.stat.Bfree * uint64(du.stat.Bsize)
}

// Available return total available bytes on file system to an unprivileged user
func (du *DiskUsage) Available() uint64 {
	return du.stat.Bavail * uint64(du.stat.Bsize)
}

// Size returns total size of the file system
func (du *DiskUsage) Size() uint64 {
	return uint64(du.stat.Blocks) * uint64(du.stat.Bsize)
}

// Used returns total bytes used in file system
func (du *DiskUsage) Used() uint64 {
	return du.Size() - du.Free()
}

// Usage returns percentage of use on the file system
func (du *DiskUsage) Usage() float32 {
	return float32(du.Used()) / float32(du.Size())
}
