// Local eSCL bridge. One scanimage process owns each complete SANE batch.
package main

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Job struct {
	ErrorHTTP                  int
	ID, Dir, State, Reason     string
	Failure                    string
	RecoveryState, RecoveryADF string
	Settings                   ScanSettings
	Started, Touched           time.Time
	Pages                      []string
	Sent                       int
	Done, Reading              bool
	Deleted                    bool
	Cmd                        *exec.Cmd
	Changed                    chan struct{}
	Cancel                     chan struct{}
	CancelOnce                 sync.Once
}
type Bridge struct {
	mu                     sync.Mutex
	Device                 Device
	WSD                    *WSDClient
	Scanimage, Probe       string
	Jobs                   map[string]*Job
	Active                 *Job
	Seq                    uint64
	ReadyAfter             time.Time
	Settle                 time.Duration
	AdfState, ScannerState string
	LastProbe              time.Time
	Closed                 bool
}

func newBridge(d Device, scanimage, probe string) *Bridge {
	return &Bridge{Device: d, Scanimage: scanimage, Probe: probe, Jobs: make(map[string]*Job), Settle: 12 * time.Second, AdfState: "ScannerAdfUnknown", ScannerState: "Idle"}
}
func (b *Bridge) notify(j *Job) { close(j.Changed); j.Changed = make(chan struct{}) }
func (b *Bridge) cancel(j *Job) {
	j.CancelOnce.Do(func() { close(j.Cancel); j.State = "Canceled"; j.Reason = "JobCanceledByUser"; b.notify(j) })
}
func (b *Bridge) jobInfo(j *Job) string {
	return fmt.Sprintf(`<scan:JobInfo><pwg:JobUri>/eSCL/ScanJobs/%s</pwg:JobUri><pwg:JobUuid>%s</pwg:JobUuid><scan:Age>%d</scan:Age><pwg:ImagesToTransfer>%d</pwg:ImagesToTransfer><pwg:ImagesCompleted>%d</pwg:ImagesCompleted><pwg:JobState>%s</pwg:JobState><pwg:JobStateReasons><pwg:JobStateReason>%s</pwg:JobStateReason></pwg:JobStateReasons></scan:JobInfo>`, j.ID, stableUUID(j.ID), int(time.Since(j.Started).Seconds()), len(j.Pages)-j.Sent, len(j.Pages), j.State, j.Reason)
}
func xmlResponse(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, s)
}
func (b *Bridge) status(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Active == nil && (b.Probe != "" || b.WSD != nil) && time.Since(b.LastProbe) > 3*time.Second && time.Now().After(b.ReadyAfter) {
		b.LastProbe = time.Now()
		if b.WSD != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			b.ScannerState, b.AdfState, _ = b.WSD.status(ctx)
			cancel()
		} else {
			data, e := outputCommand(b.Probe, b.Device.URI)
			if e != nil {
				b.ScannerState = "Stopped"
				b.AdfState = "ScannerAdfUnknown"
			} else {
				b.ScannerState, b.AdfState = parseStatus(data)
			}
		}
	}
	state, adf := b.ScannerState, b.AdfState
	if b.Active != nil {
		state = "Processing"
		if b.Active.Settings.InputSource == "ADF" {
			adf = "ScannerAdfProcessing"
		}
	}
	var s strings.Builder
	fmt.Fprintf(&s, `<?xml version="1.0"?><scan:ScannerStatus xmlns:scan="%s" xmlns:pwg="%s"><pwg:Version>2.6</pwg:Version><pwg:State>%s</pwg:State><scan:AdfState>%s</scan:AdfState><scan:Jobs>`, scanNS, pwgNS, state, adf)
	for _, j := range b.Jobs {
		s.WriteString(b.jobInfo(j))
	}
	s.WriteString("</scan:Jobs></scan:ScannerStatus>")
	xmlResponse(w, s.String())
}
func parseStatus(data []byte) (string, string) {
	state, adf, reason := "Stopped", "ScannerAdfUnknown", ""
	seenState := false
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		t, e := dec.Token()
		if e != nil {
			if e != io.EOF {
				return "Stopped", "ScannerAdfUnknown"
			}
			break
		}
		if el, ok := t.(xml.StartElement); ok {
			switch el.Name.Local {
			case "PaperInADF", "ScannerState", "ScannerStateReason":
				var v string
				if dec.DecodeElement(&v, &el) != nil {
					return "Stopped", "ScannerAdfUnknown"
				}
				v = strings.TrimSpace(v)
				switch el.Name.Local {
				case "PaperInADF":
					if v == "true" || v == "1" {
						adf = "ScannerAdfLoaded"
					} else if v == "false" || v == "0" {
						adf = "ScannerAdfEmpty"
					}
				case "ScannerState":
					seenState = true
					if v == "Idle" {
						state = "Idle"
					} else if v == "Processing" {
						state = "Processing"
					}
				case "ScannerStateReason":
					reason = v
				}
			}
		}
	}
	if !seenState {
		return "Stopped", "ScannerAdfUnknown"
	}
	if reason == "MediaJam" {
		adf = "ScannerAdfJam"
		state = "Stopped"
	}
	return state, adf
}

type responseTrace struct {
	http.ResponseWriter
	code, bytes int
}

func (w *responseTrace) WriteHeader(code int) {
	if w.code != 0 {
		return
	}
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *responseTrace) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("AIRSCAN_DEBUG") == "1" {
		start := time.Now()
		trace := &responseTrace{ResponseWriter: w}
		w = trace
		defer func() {
			code := trace.code
			if code == 0 {
				code = http.StatusOK
			}
			log.Printf("HTTP %s %s -> %d bytes=%d elapsed=%s ua=%q", r.Method, r.URL.Path, code, trace.bytes, time.Since(start).Round(time.Millisecond), r.UserAgent())
		}()
	}
	w.Header().Set("Cache-Control", "no-store")
	// Browser origins cannot start scans. Native eSCL clients do not send Origin.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser cross-origin access is disabled", 403)
		return
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/eSCL/ScannerCapabilities":
			xmlResponse(w, b.Device.capabilities())
			return
		case "/eSCL/ScannerStatus":
			b.status(w, r)
			return
		case "/":
			fmt.Fprintf(w, "HP SOAPHT AirScan bridge\n%s\n", b.Device.Model)
			return
		}
	}
	if r.URL.Path == "/eSCL/ScanJobs" {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		b.create(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/eSCL/ScanJobs/") {
		b.item(w, r)
		return
	}
	http.NotFound(w, r)
}
func (b *Bridge) create(w http.ResponseWriter, r *http.Request) {
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if e != nil {
		http.Error(w, "request too large", 413)
		return
	}
	s, e := parseSettings(data, b.Device)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Closed || b.Active != nil || len(b.Jobs) >= 4 {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "scanner busy", 503)
		return
	}
	dir, e := os.MkdirTemp("", "hp-airscan-job-")
	if e != nil {
		http.Error(w, "cannot create spool", 500)
		return
	}
	b.Seq++
	id := fmt.Sprintf("%s-%d", stableUUID(dir)[:8], b.Seq)
	j := &Job{ID: id, Dir: dir, Settings: s, State: "Pending", Reason: "JobQueued", Started: time.Now(), Touched: time.Now(), Changed: make(chan struct{}), Cancel: make(chan struct{})}
	b.Jobs[id] = j
	b.Active = j
	w.Header().Set("Location", "http://"+r.Host+"/eSCL/ScanJobs/"+id)
	w.WriteHeader(201)
	if os.Getenv("AIRSCAN_DEBUG") == "1" {
		log.Printf("job %s settings: source=%s dpi=%d mode=%s region=%+v", id, s.InputSource, s.XResolution, s.ColorMode, s.Regions[0])
	}
	go b.run(j)
}
func (b *Bridge) run(j *Job) {
	b.mu.Lock()
	delay := time.Until(b.ReadyAfter)
	b.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-j.Cancel:
			b.finish(j, nil, "")
			return
		}
	}
	select {
	case <-j.Cancel:
		b.finish(j, nil, "")
		return
	default:
	}
	if b.WSD != nil {
		b.runWSD(j)
		return
	}
	cmd := exec.Command(b.Scanimage, j.Settings.args(b.Device.URI, j.Dir)...)
	cmd.Env = commandEnv()
	if os.Getenv("AIRSCAN_DEBUG") == "1" {
		cmd.Env = append(cmd.Env, "SANE_DEBUG_HPAIO=6")
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		b.finish(j, e, "")
		return
	}
	stderr := &limitedLog{}
	cmd.Stderr = stderr
	b.mu.Lock()
	if j.State == "Canceled" {
		b.mu.Unlock()
		b.finish(j, nil, "")
		return
	}
	j.Cmd = cmd
	e = cmd.Start()
	if e == nil {
		j.State = "Processing"
		j.Reason = "JobScanning"
	}
	b.notify(j)
	b.mu.Unlock()
	if e != nil {
		b.finish(j, e, "")
		return
	}
	exited := make(chan struct{})
	go func() {
		select {
		case <-j.Cancel:
		case <-time.After(15 * time.Minute):
			b.mu.Lock()
			if !j.Done {
				j.Failure = "JobTimedOut"
				b.cancel(j)
			}
			b.mu.Unlock()
		case <-exited:
			return
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(320 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	var total int64
	var pageErr error
	for scanner.Scan() {
		name := scanner.Text()
		expected := filepath.Join(j.Dir, fmt.Sprintf("page-%04d.jpg", len(j.Pages)+1))
		if name != expected {
			pageErr = fmt.Errorf("unexpected spool path")
			break
		}
		info, e := os.Stat(name)
		if e != nil || info.Size() > 32<<20 {
			pageErr = fmt.Errorf("invalid page size")
			break
		}
		total += info.Size()
		if total > 256<<20 || len(j.Pages) >= 100 {
			pageErr = fmt.Errorf("batch limit exceeded")
			break
		}
		f, e := os.Open(name)
		if e != nil {
			pageErr = e
			break
		}
		cfg, e := jpeg.DecodeConfig(f)
		f.Close()
		if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 150000000 {
			pageErr = fmt.Errorf("invalid JPEG page")
			break
		}
		b.mu.Lock()
		j.Pages = append(j.Pages, name)
		b.notify(j)
		b.mu.Unlock()
	}
	if scanner.Err() != nil {
		pageErr = scanner.Err()
	}
	if pageErr != nil {
		b.mu.Lock()
		j.Failure = "ErrorsDetected"
		b.cancel(j)
		b.mu.Unlock()
	}
	e = cmd.Wait()
	close(exited)
	if pageErr != nil {
		e = pageErr
	}
	// Keep Active set while recovering: neither another scan nor status probe
	// may consume the pending reply before the drain reaches a quiet boundary.
	if e != nil && b.Probe != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		recovery := exec.CommandContext(ctx, b.Probe, "--recover", b.Device.URI)
		recovery.Env = commandEnv()
		var note limitedLog
		recovery.Stderr = &note
		data, recoveryErr := recovery.Output()
		cancel()
		log.Printf("job %s transport recovery: %v; %s", j.ID, recoveryErr, strings.TrimSpace(note.String()))
		b.mu.Lock()
		if recoveryErr == nil {
			j.RecoveryState, j.RecoveryADF = parseStatus(data)
		}
		b.mu.Unlock()
	}
	b.finish(j, e, stderr.String())
}

type limitedLog struct {
	mu   sync.Mutex
	text string
}

func (l *limitedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text += string(p)
	if len(l.text) > 16384 {
		l.text = l.text[len(l.text)-16384:]
	}
	return len(p), nil
}
func (l *limitedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.text }
func (b *Bridge) finish(j *Job, err error, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j.Done = true
	j.Cmd = nil
	if b.Active == j {
		b.Active = nil
	}
	b.ReadyAfter = time.Now().Add(b.Settle)
	b.LastProbe = time.Time{}
	if j.Failure != "" {
		j.State = "Aborted"
		j.ErrorHTTP = 500
		j.Reason = j.Failure
		b.ScannerState = "Stopped"
	}
	if j.State != "Canceled" {
		if err != nil || j.Failure != "" {
			j.State = "Aborted"
			j.ErrorHTTP = 500
			if j.Failure == "" {
				j.Reason = "ErrorsDetected"
			}
			b.ScannerState = "Stopped"
			switch {
			case strings.Contains(detail, "out of documents"):
				b.AdfState = "ScannerAdfEmpty"
				j.ErrorHTTP = 404
			case strings.Contains(detail, "jammed"):
				b.AdfState = "ScannerAdfJam"
			}
			log.Printf("job %s failed: %v; %s", j.ID, err, strings.TrimSpace(detail))
		} else {
			j.State = "Processing"
			j.Reason = "JobCompletedSuccessfully"
			b.ScannerState = "Idle"
			if j.Settings.InputSource == "ADF" && strings.Contains(detail, "out of documents") {
				b.AdfState = "ScannerAdfEmpty"
			}
			if j.Sent == len(j.Pages) {
				j.State = "Completed"
			}
		}
	}
	if j.RecoveryState != "" {
		b.ScannerState, b.AdfState = j.RecoveryState, j.RecoveryADF
	}
	b.notify(j)
	if j.Deleted && !j.Reading {
		delete(b.Jobs, j.ID)
		os.RemoveAll(j.Dir)
	}
	log.Printf("job %s acquisition ended: %s, %d pages", j.ID, j.State, len(j.Pages))
}
func (b *Bridge) item(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/eSCL/ScanJobs/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	b.mu.Lock()
	j := b.Jobs[parts[0]]
	if j == nil {
		b.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	j.Touched = time.Now()
	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			j.Deleted = true
			b.cancel(j)
			if j.Done && !j.Reading {
				delete(b.Jobs, j.ID)
				os.RemoveAll(j.Dir)
			}
			b.mu.Unlock()
			w.WriteHeader(200)
			return
		}
		if r.Method != http.MethodGet {
			b.mu.Unlock()
			w.WriteHeader(405)
			return
		}
		s := fmt.Sprintf(`<scan:ScanJob xmlns:scan="%s" xmlns:pwg="%s">%s</scan:ScanJob>`, scanNS, pwgNS, b.jobInfo(j))
		b.mu.Unlock()
		xmlResponse(w, s)
		return
	}
	if parts[1] != "NextDocument" {
		b.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		b.mu.Unlock()
		w.WriteHeader(405)
		return
	}
	if j.Reading {
		b.mu.Unlock()
		http.Error(w, "page request already active", 409)
		return
	}
	j.Reading = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		j.Reading = false
		if j.Deleted && j.Done {
			delete(b.Jobs, j.ID)
			os.RemoveAll(j.Dir)
		}
		b.mu.Unlock()
	}()
	// Apple AirScan can defer its cancel request while NextDocument is held
	// open. Bound each wait so the client can process cancel between retries.
	pending := time.NewTimer(2 * time.Second)
	defer pending.Stop()
	for {
		b.mu.Lock()
		if j.State == "Canceled" && j.Failure == "" {
			b.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if j.Sent < len(j.Pages) {
			path := j.Pages[j.Sent]
			b.mu.Unlock()
			f, e := os.Open(path)
			if e != nil {
				http.Error(w, "page unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, e = io.Copy(w, f)
			f.Close()
			b.mu.Lock()
			if e == nil {
				j.Sent++
				j.Touched = time.Now()
				if j.Done && j.Sent == len(j.Pages) && j.State != "Aborted" {
					j.State = "Completed"
				}
			} else {
				b.cancel(j)
			}
			b.mu.Unlock()
			return
		}
		if j.Done || j.Failure != "" {
			aborted := j.State == "Aborted" || j.Failure != ""
			code := j.ErrorHTTP
			if code == 0 {
				code = 500
			}
			b.mu.Unlock()
			if aborted {
				http.Error(w, "scan acquisition failed; see ScannerStatus", code)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		changed := j.Changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-pending.C:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "image acquisition in progress", http.StatusServiceUnavailable)
			return
		case <-r.Context().Done():
			b.mu.Lock()
			b.cancel(j)
			b.mu.Unlock()
			return
		}
	}
}
func (b *Bridge) reap() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, j := range b.Jobs {
		if time.Since(j.Touched) > 2*time.Minute && !j.Reading {
			if j.Done {
				os.RemoveAll(j.Dir)
				delete(b.Jobs, id)
			} else {
				b.cancel(j)
			}
		}
	}
}
func (b *Bridge) shutdown() {
	b.mu.Lock()
	b.Closed = true
	for _, j := range b.Jobs {
		if !j.Done {
			b.cancel(j)
		}
	}
	b.mu.Unlock()
}
func advertise(ctx context.Context, d Device, port int) error {
	uuid := stableUUID(d.URI)
	host := "hp-soapht-" + uuid[:8] + ".local"
	sources := []string{}
	colors := []string{}
	seen := map[string]bool{}
	for _, s := range d.Sources {
		if s.Name == "Flatbed" {
			sources = append(sources, "platen")
		} else {
			sources = append(sources, "adf")
		}
		for _, m := range s.Modes {
			v := "grayscale"
			if m == "RGB24" {
				v = "color"
			}
			if !seen[v] {
				colors = append(colors, v)
				seen[v] = true
			}
		}
	}
	label := "SOAPHT"
	if d.Transport != "" {
		label = d.Transport
	}
	args := []string{"-lo", "-P", d.Model + " (" + label + ")", "_uscan._tcp", "local", strconv.Itoa(port), host, "127.0.0.1", "txtvers=1", "vers=2.6", "ty=" + d.Model, "UUID=" + uuid, "rs=eSCL", "pdl=image/jpeg", "cs=" + strings.Join(colors, ","), "is=" + strings.Join(sources, ","), "duplex=F"}
	cmd := exec.CommandContext(ctx, "/usr/bin/dns-sd", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	scanimage := os.Getenv("AIRSCAN_SCANIMAGE")
	if scanimage == "" {
		scanimage = "/opt/homebrew/bin/scanimage"
	}
	probe := os.Getenv("AIRSCAN_PROBE")
	if probe == "" {
		probe = "/opt/homebrew/bin/hp-soapht-probe"
	}
	var d Device
	var e error
	var wsd *WSDClient
	if endpoint := os.Getenv("AIRSCAN_WSD_URL"); endpoint != "" {
		if os.Getenv("AIRSCAN_DEVICE") != "" {
			log.Fatal("choose either AIRSCAN_WSD_URL or AIRSCAN_DEVICE")
		}
		wsd, e = newWSDClient(endpoint)
		if e != nil {
			log.Fatal(e)
		}
		d, e = waitWSDDevice(ctx, wsd.device, 30*time.Second)
		if e != nil {
			return
		}
	} else {
		d, e = detectDevice(scanimage, os.Getenv("AIRSCAN_DEVICE"))
		if e != nil {
			log.Fatal(e)
		}
		// The probe validates HPLIP scan-type=5 before any Bonjour advertisement.
		caps, e := outputCommand(probe, d.URI)
		if e != nil {
			log.Fatalf("SOAPHT capability probe: %v", e)
		}
		if e = d.applyMinimums(caps); e != nil {
			log.Fatalf("SOAPHT minimum geometry: %v", e)
		}

	}
	if name := strings.TrimSpace(os.Getenv("AIRSCAN_NAME")); name != "" {
		if len(name) > 80 {
			log.Fatal("AIRSCAN_NAME must be at most 80 bytes")
		}
		d.Model = name
	}
	b := newBridge(d, scanimage, probe)
	b.WSD = wsd

	port := 8089
	if v := os.Getenv("AIRSCAN_PORT"); v != "" {
		port, e = strconv.Atoi(v)
		if e != nil || port < 1024 || port > 65535 {
			log.Fatal("invalid AIRSCAN_PORT")
		}
	}
	listener, e := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil {
		log.Fatal(e)
	}
	srv := &http.Server{Handler: b, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if e := srv.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
			log.Print(e)
			cancel()
		}
	}()
	go func() {
		for ctx.Err() == nil {
			if e := advertise(ctx, d, port); e != nil && ctx.Err() == nil {
				log.Printf("Bonjour: %v", e)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.reap()
			}
		}
	}()
	log.Printf("%s: local eSCL ready on 127.0.0.1:%d", d.Model, port)
	<-ctx.Done()
	b.shutdown()
	shutdown, c := context.WithTimeout(context.Background(), 390*time.Second)
	defer c()
	_ = srv.Shutdown(shutdown)
	// Wait for native CancelJob cleanup even when no HTTP request is active.
	for {
		b.mu.Lock()
		active := b.Active != nil
		b.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-shutdown.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	b.mu.Lock()
	for _, j := range b.Jobs {
		os.RemoveAll(j.Dir)
	}
	b.mu.Unlock()
}
