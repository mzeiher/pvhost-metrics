package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	volume_stat_size_bytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_size_bytes",
		Help: "Size of all files in the path of the volume",
	}, []string{"host_mount_path", "host_device", "path"})
	volume_stat_files = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_files",
		Help: "number of files in the directory",
	}, []string{"host_mount_path", "host_device", "path"})
	volume_stat_directories = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_directories",
		Help: "number of directories in the directory",
	}, []string{"host_mount_path", "host_device", "path"})
	volume_stat_errors = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_errors",
		Help: "number of errors while reading files",
	}, []string{"host_mount_path", "host_device", "path"})
	volume_stat_runtime_seconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_runtime_seconds",
		Help: "stat time in microseconds",
	}, []string{"host_mount_path", "host_device", "path"})
	volume_stat_blocks_available_bytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_available_bytes",
		Help: "available blocks on path",
	}, []string{"host_device"})
	volume_stat_blocks_free_bytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_free_byte",
		Help: "free blocks on path",
	}, []string{"host_device"})
	volume_stat_blocks_used_bytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_used_bytes",
		Help: "used blocks on path",
	}, []string{"host_device"})
	volume_stat_blocks_size_bytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "volume_stat_blocks_size_bytes",
		Help: "size of blocks on volume in path",
	}, []string{"host_device"})
)

func main() {

	port := flag.Int("port", 8080, "port to use")
	host := flag.String("host", "", "host to use")
	flag.Parse()

	var path = flag.Arg(0)
	if path == "" {
		panic("no path defined")
	}

	ticker := time.NewTicker(60 * time.Second)

	mountInfo := NewMountInfo(path)

	UpdateInfo(path, mountInfo)

	go func() {
		for range ticker.C {
			UpdateInfo(path, mountInfo)
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

	sigs := make(chan os.Signal, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	ticker.Stop()
}

func UpdateInfo(path string, mountInfo MountInfo) {
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

	volume_stat_runtime_seconds.With(prometheus.Labels{"host_mount_path": mountInfo.mountPath, "host_device": mountInfo.hostDevice, "path": mountInfo.path}).Set(float64(runtime / 1000000))
	volume_stat_size_bytes.With(prometheus.Labels{"host_mount_path": mountInfo.mountPath, "host_device": mountInfo.hostDevice, "path": mountInfo.path}).Set(float64(size))
	volume_stat_files.With(prometheus.Labels{"host_mount_path": mountInfo.mountPath, "host_device": mountInfo.hostDevice, "path": mountInfo.path}).Set(float64(files))
	volume_stat_directories.With(prometheus.Labels{"host_mount_path": mountInfo.mountPath, "host_device": mountInfo.hostDevice, "path": mountInfo.path}).Set(float64(folders))
	volume_stat_errors.With(prometheus.Labels{"host_mount_path": mountInfo.mountPath, "host_device": mountInfo.hostDevice, "path": mountInfo.path}).Set(float64(errors))
	volume_stat_blocks_available_bytes.With(prometheus.Labels{"host_device": mountInfo.hostDevice}).Set(float64(usage.Available()))
	volume_stat_blocks_free_bytes.With(prometheus.Labels{"host_device": mountInfo.hostDevice}).Set(float64(usage.Free()))
	volume_stat_blocks_used_bytes.With(prometheus.Labels{"host_device": mountInfo.hostDevice}).Set(float64(usage.Used()))
	volume_stat_blocks_size_bytes.With(prometheus.Labels{"host_device": mountInfo.hostDevice}).Set(float64(usage.Size()))
}

type MountInfo struct {
	mountPath  string
	hostDevice string
	path       string
}

func NewMountInfo(volumePath string) MountInfo {

	absPath, _ := filepath.Abs(volumePath)

	fmt.Printf("getting mount info for %s\n", absPath)

	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return MountInfo{mountPath: "unknown", hostDevice: "unknown"}
	}
	defer file.Close()

	fileScanner := bufio.NewScanner(file)
	fileScanner.Split(bufio.ScanLines)

	bestMatchLength := 0
	bestMatch := MountInfo{mountPath: "unknown", hostDevice: "unknown"}

	for fileScanner.Scan() {
		matcher := regexp.MustCompile(`\d+ \d+ ((\d+):(\d+)) ([^ ]+) ([^ ]+) [^-]+ - ([^ ]+) ([^ ]+)`)
		mountInfoLine := fileScanner.Text()
		subMatch := matcher.FindStringSubmatch(mountInfoLine)
		if subMatch != nil {
			mountPath := subMatch[5]
			fmt.Printf("checking if \"%s\" is in path \"%s\"\n", absPath, mountPath)
			if strings.HasPrefix(absPath, mountPath) && len(mountPath) > bestMatchLength {
				fmt.Printf("new best match found: %s\n", mountPath)
				bestMatch = MountInfo{
					mountPath:  subMatch[4],
					hostDevice: subMatch[7],
					path:       absPath,
				}
				bestMatchLength = len(mountPath)
			}
		}
	}
	return bestMatch
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

func (du *DiskUsage) FsId() (int32, int32) {
	return du.stat.Fsid.X__val[0], du.stat.Fsid.X__val[1]
}
