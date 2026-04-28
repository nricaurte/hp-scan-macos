// airscan-bridge: a tiny eSCL/AirScan front-end that translates network
// scan requests from macOS apps (Image Capture, Preview, HP Easy Scan,
// iPhone/iPad scan) into local `scanimage` invocations against a SANE
// backend on the same Mac.
//
// Apple's native scan stack uses ICA, which only sees scanners exposed
// by vendor drivers in /Library/Image Capture/Devices/. AirScan/eSCL is
// the parallel network-scan path, and Image Capture treats any host
// advertising _uscan._tcp on the LAN as a real scanner. By advertising
// ourselves on the loopback interface, we appear in every Apple scan
// app without needing an ICA driver.

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// -------- scanner detection (via SANE) --------

var deviceURI string

func detectDevice() string {
	if v := os.Getenv("AIRSCAN_DEVICE"); v != "" {
		return v
	}
	out, err := exec.Command("scanimage", "-L").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile("device [`']([^`']+)[`']")
	if m := re.FindStringSubmatch(string(out)); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func deviceModel(uri string) string {
	// hpaio:/usb/Smart_Tank_500_series?serial=... -> "Smart Tank 500 series"
	re := regexp.MustCompile(`/usb/([^?]+)`)
	m := re.FindStringSubmatch(uri)
	if len(m) < 2 {
		return "Bridged Scanner"
	}
	return strings.ReplaceAll(m[1], "_", " ")
}

// -------- jobs --------

type Job struct {
	ID         string
	State      string // Pending, Processing, Completed, Aborted, Canceled
	Settings   ScanSettings
	Started    time.Time
	dataReader io.ReadCloser
	scanCmd    *exec.Cmd
	once       sync.Once
}

type ScanSettings struct {
	XMLName        xml.Name `xml:"ScanSettings"`
	Version        string   `xml:"Version"`
	ColorMode      string   `xml:"ColorMode"`
	DocumentFormat string   `xml:"DocumentFormat"`
	XResolution    int      `xml:"XResolution"`
	YResolution    int      `xml:"YResolution"`
	InputSource    string   `xml:"InputSource"`
}

var (
	jobs   sync.Map // jobID -> *Job
	jobSeq int64
)

// -------- handlers --------

func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	model := deviceModel(deviceURI)
	uuid := stableUUID(deviceURI)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<scan:ScannerCapabilities xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
  <pwg:Version>2.6</pwg:Version>
  <pwg:MakeAndModel>%s</pwg:MakeAndModel>
  <pwg:SerialNumber>airscan-bridge</pwg:SerialNumber>
  <scan:UUID>%s</scan:UUID>
  <scan:AdminURI>http://127.0.0.1:%d/</scan:AdminURI>
  <scan:Platen>
    <scan:PlatenInputCaps>
      <scan:MinWidth>16</scan:MinWidth>
      <scan:MaxWidth>2550</scan:MaxWidth>
      <scan:MinHeight>16</scan:MinHeight>
      <scan:MaxHeight>3508</scan:MaxHeight>
      <scan:MaxScanRegions>1</scan:MaxScanRegions>
      <scan:SettingProfiles>
        <scan:SettingProfile>
          <scan:ColorModes>
            <scan:ColorMode>BlackAndWhite1</scan:ColorMode>
            <scan:ColorMode>Grayscale8</scan:ColorMode>
            <scan:ColorMode>RGB24</scan:ColorMode>
          </scan:ColorModes>
          <scan:DocumentFormats>
            <pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>
            <pwg:DocumentFormat>application/pdf</pwg:DocumentFormat>
          </scan:DocumentFormats>
          <scan:DocumentFormatsExt>
            <scan:DocumentFormatExt>image/jpeg</scan:DocumentFormatExt>
            <scan:DocumentFormatExt>application/pdf</scan:DocumentFormatExt>
          </scan:DocumentFormatsExt>
          <scan:SupportedResolutions>
            <scan:DiscreteResolutions>
              <scan:DiscreteResolution><scan:XResolution>75</scan:XResolution><scan:YResolution>75</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>100</scan:XResolution><scan:YResolution>100</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>150</scan:XResolution><scan:YResolution>150</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>200</scan:XResolution><scan:YResolution>200</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>300</scan:XResolution><scan:YResolution>300</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>600</scan:XResolution><scan:YResolution>600</scan:YResolution></scan:DiscreteResolution>
              <scan:DiscreteResolution><scan:XResolution>1200</scan:XResolution><scan:YResolution>1200</scan:YResolution></scan:DiscreteResolution>
            </scan:DiscreteResolutions>
          </scan:SupportedResolutions>
          <scan:ColorSpaces><scan:ColorSpace>RGB</scan:ColorSpace></scan:ColorSpaces>
        </scan:SettingProfile>
      </scan:SettingProfiles>
    </scan:PlatenInputCaps>
  </scan:Platen>
</scan:ScannerCapabilities>
`, model, uuid, port)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<scan:ScannerStatus xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
  <pwg:Version>2.6</pwg:Version>
  <pwg:State>Idle</pwg:State>
  <scan:AdfState>ScannerAdfEmpty</scan:AdfState>
  <scan:Jobs>`)
	jobs.Range(func(k, v interface{}) bool {
		j := v.(*Job)
		fmt.Fprintf(w, `
    <scan:JobInfo>
      <pwg:JobUri>/eSCL/ScanJobs/%s</pwg:JobUri>
      <pwg:JobUuid>%s</pwg:JobUuid>
      <scan:Age>%d</scan:Age>
      <scan:ImagesToTransfer>1</scan:ImagesToTransfer>
      <scan:ImagesCompleted>0</scan:ImagesCompleted>
      <pwg:JobState>%s</pwg:JobState>
      <pwg:JobStateReasons><pwg:JobStateReason>JobScanning</pwg:JobStateReason></pwg:JobStateReasons>
    </scan:JobInfo>`, j.ID, stableUUID(j.ID), int(time.Since(j.Started).Seconds()), j.State)
		return true
	})
	fmt.Fprint(w, `
  </scan:Jobs>
</scan:ScannerStatus>
`)
}

// loggingHandler wraps an http.Handler and logs every request.
func loggingHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("HTTP %s %s  ua=%q", r.Method, r.URL.Path, r.Header.Get("User-Agent"))
		h.ServeHTTP(w, r)
	})
}

func handleScanJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var s ScanSettings
	if err := xml.Unmarshal(body, &s); err != nil {
		log.Printf("bad ScanSettings xml: %v\nbody=%q", err, body)
		http.Error(w, "bad xml", http.StatusBadRequest)
		return
	}
	if s.XResolution == 0 {
		s.XResolution = 300
	}
	if s.DocumentFormat == "" {
		s.DocumentFormat = "image/jpeg"
	}
	if s.ColorMode == "" {
		s.ColorMode = "RGB24"
	}
	id := strconv.FormatInt(atomic.AddInt64(&jobSeq, 1), 10)
	job := &Job{
		ID:       id,
		State:    "Processing",
		Settings: s,
		Started:  time.Now(),
	}
	jobs.Store(id, job)
	log.Printf("POST /eSCL/ScanJobs -> job %s (%dDPI %s -> %s)", id, s.XResolution, s.ColorMode, s.DocumentFormat)
	host := r.Host
	if host == "" {
		host = fmt.Sprintf("127.0.0.1:%d", port)
	}
	// Apple's Image Capture requires an absolute Location URL.
	w.Header().Set("Location", fmt.Sprintf("http://%s/eSCL/ScanJobs/%s", host, id))
	w.WriteHeader(http.StatusCreated)
}

func writeJobInfo(w http.ResponseWriter, job *Job) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	images := 0
	if job.State == "Completed" {
		images = 1
	}
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<scan:ScanJob xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
  <pwg:JobUri>/eSCL/ScanJobs/%s</pwg:JobUri>
  <pwg:JobUuid>%s</pwg:JobUuid>
  <scan:Age>%d</scan:Age>
  <scan:ImagesToTransfer>1</scan:ImagesToTransfer>
  <scan:ImagesCompleted>%d</scan:ImagesCompleted>
  <pwg:JobState>%s</pwg:JobState>
  <pwg:JobStateReasons>
    <pwg:JobStateReason>JobScanning</pwg:JobStateReason>
  </pwg:JobStateReasons>
</scan:ScanJob>
`, job.ID, stableUUID(job.ID), int(time.Since(job.Started).Seconds()), images, job.State)
}

func handleScanJobItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/eSCL/ScanJobs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		log.Printf("%s %s -> 404 (empty id)", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	log.Printf("%s %s", r.Method, r.URL.Path)
	v, ok := jobs.Load(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	job := v.(*Job)

	if r.Method == http.MethodDelete {
		job.once.Do(func() {
			if job.scanCmd != nil && job.scanCmd.Process != nil {
				_ = job.scanCmd.Process.Kill()
			}
			if job.dataReader != nil {
				_ = job.dataReader.Close()
			}
		})
		jobs.Delete(id)
		w.WriteHeader(http.StatusOK)
		return
	}

	// GET /eSCL/ScanJobs/{id}  — return JobInfo XML
	if len(parts) < 2 || parts[1] == "" {
		writeJobInfo(w, job)
		return
	}

	if parts[1] != "NextDocument" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// NextDocument is single-shot for platen; second call → no more pages.
	if job.State == "Completed" {
		http.NotFound(w, r)
		return
	}

	if err := streamScan(w, r, job); err != nil {
		log.Printf("job %s scan error: %v", id, err)
	}
	job.State = "Completed"
}

// streamScan invokes scanimage and pipes its output to the HTTP client.
func streamScan(w http.ResponseWriter, r *http.Request, job *Job) error {
	mode := "Color"
	switch job.Settings.ColorMode {
	case "Grayscale8":
		mode = "Gray"
	case "BlackAndWhite1":
		mode = "Lineart"
	}
	res := strconv.Itoa(job.Settings.XResolution)
	wantPDF := job.Settings.DocumentFormat == "application/pdf"

	args := []string{
		"-d", deviceURI,
		"--mode", mode,
		"--resolution", res,
		"--format", "jpeg",
	}
	cmd := exec.CommandContext(r.Context(), "scanimage", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	job.scanCmd = cmd
	job.dataReader = stdout

	log.Printf("job %s: starting scanimage %v", job.ID, args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Stream depending on format
	if !wantPDF {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, stdout); err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("copy jpeg: %w", err)
		}
		return cmd.Wait()
	}

	// PDF: capture jpeg to temp file, then sips convert
	tmpDir, err := os.MkdirTemp("", "airscan-bridge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	jpgPath := tmpDir + "/scan.jpg"
	pdfPath := tmpDir + "/scan.pdf"
	jpgFile, err := os.Create(jpgPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(jpgFile, stdout); err != nil {
		jpgFile.Close()
		return err
	}
	jpgFile.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("scanimage exited: %w", err)
	}
	if out, err := exec.Command("sips", "-s", "format", "pdf", jpgPath, "--out", pdfPath).CombinedOutput(); err != nil {
		return fmt.Errorf("sips: %w (%s)", err, out)
	}
	pdfFile, err := os.Open(pdfPath)
	if err != nil {
		return err
	}
	defer pdfFile.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, pdfFile)
	return err
}

// stableUUID derives a deterministic uuid-ish string from a URI.
func stableUUID(uri string) string {
	// not crypto, just stable across restarts
	h := uint64(1469598103934665603)
	for _, b := range []byte(uri) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	s := fmt.Sprintf("%016x", h)
	// pad
	for len(s) < 32 {
		s += s
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// -------- mDNS via dns-sd subprocess --------

var port = 8089

func advertise(ctx context.Context, model, uuid string) error {
	// _uscan._tcp = AirScan/Mopria scan
	args := []string{
		"-R",
		fmt.Sprintf("%s (USB-bridge)", model),
		"_uscan._tcp",
		"local",
		strconv.Itoa(port),
		"txtvers=1",
		"vers=2.6",
		"ty=" + model,
		"note=via airscan-bridge",
		"adminurl=http://127.0.0.1:" + strconv.Itoa(port) + "/",
		"representation=http://127.0.0.1:" + strconv.Itoa(port) + "/icon.png",
		"UUID=" + uuid,
		"rs=eSCL",
		"pdl=image/jpeg,application/pdf",
		"cs=color,grayscale,binary",
		"is=platen",
		"duplex=F",
	}
	cmd := exec.CommandContext(ctx, "dns-sd", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	log.Printf("advertising _uscan._tcp on port %d as %q", port, model)
	return cmd.Run()
}

// -------- main --------

func main() {
	deviceURI = detectDevice()
	if deviceURI == "" {
		log.Fatal("no SANE scanner detected via `scanimage -L`. Ensure your printer is connected and the hpaio backend is installed.")
	}
	log.Printf("scanner: %s (%s)", deviceURI, deviceModel(deviceURI))

	if envPort := os.Getenv("AIRSCAN_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/eSCL/ScannerCapabilities", handleCapabilities)
	mux.HandleFunc("/eSCL/ScannerStatus", handleStatus)
	mux.HandleFunc("/eSCL/ScanJobs", handleScanJobs)
	mux.HandleFunc("/eSCL/ScanJobs/", handleScanJobItem)
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		// 1x1 transparent PNG so Image Capture has something to render.
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
			0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
			0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
			0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
			0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "airscan-bridge\nscanner: %s\n", deviceURI)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		// keep advertising; restart on failure
		for ctx.Err() == nil {
			if err := advertise(ctx, deviceModel(deviceURI), stableUUID(deviceURI)); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
				log.Printf("dns-sd exited: %v; respawning in 2s", err)
				time.Sleep(2 * time.Second)
			}
		}
	}()

	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: loggingHandler(mux)}
	go func() {
		log.Printf("eSCL HTTP listening on http://127.0.0.1:%d", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")
	cancel()
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer c2()
	srv.Shutdown(shutdownCtx)
}
