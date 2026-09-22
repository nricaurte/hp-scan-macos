package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testDevice(t *testing.T) Device {
	t.Helper()
	d := Device{URI: "hpaio:/usb/Test?serial=FAKE", Model: "HP <Test> & Scanner"}
	for _, name := range []string{"Flatbed", "ADF"} {
		v, e := os.ReadFile("testdata/" + name + ".txt")
		if e != nil {
			t.Fatal(e)
		}
		s, e := parseSource(name, string(v))
		if e != nil {
			t.Fatal(e)
		}
		d.Sources = append(d.Sources, s)
	}
	return d
}
func settings(source string) string {
	return `<scan:ScanSettings xmlns:scan="` + scanNS + `" xmlns:pwg="` + pwgNS + `"><scan:InputSource>` + source + `</scan:InputSource><scan:ColorMode>Grayscale8</scan:ColorMode><scan:XResolution>300</scan:XResolution><scan:YResolution>300</scan:YResolution><pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat></scan:ScanSettings>`
}
func TestCapabilitiesAndGeometry(t *testing.T) {
	d := testDevice(t)
	if d.Sources[0].Height != 3507 || d.Sources[1].Height != 4200 {
		t.Fatal(d)
	}
	dec := xml.NewDecoder(strings.NewReader(d.capabilities()))
	for {
		_, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	s := d.capabilities()
	if strings.Contains(s, "BlackAndWhite1") || strings.Contains(s, "1200") || strings.Contains(s, "application/pdf") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "HP &lt;Test&gt; &amp; Scanner") {
		t.Fatal(s)
	}
	for _, tc := range []struct {
		h      int
		source string
		ok     bool
	}{{3507, "Platen", true}, {3508, "Platen", false}, {3508, "Feeder", true}, {3300, "Feeder", true}, {4200, "Feeder", true}, {4201, "Feeder", false}} {
		body := strings.Replace(settings(tc.source), "</scan:ScanSettings>", fmt.Sprintf(`<scan:ScanRegions><scan:ScanRegion><pwg:Width>2480</pwg:Width><pwg:Height>%d</pwg:Height></scan:ScanRegion></scan:ScanRegions></scan:ScanSettings>`, tc.h), 1)
		p, e := parseSettings([]byte(body), d)
		if (e == nil) != tc.ok {
			t.Fatalf("%+v: %v", tc, e)
		}
		if e == nil && p.Regions[0].Height != tc.h {
			t.Fatal(p)
		}
	}
	p, e := parseSettings([]byte(settings("Feeder")), d)
	if e != nil || p.Regions[0].Height != 3506 {
		t.Fatal(p, e)
	}
	args := strings.Join(p.args(d.URI, "/tmp/job"), " ")
	if !strings.Contains(args, "--source ADF") || !strings.Contains(args, "--batch-print") || strings.Contains(args, "--batch-count") {
		t.Fatal(args)
	}
	// Alternate source-only and wider/DPI fixtures exercise model-independent paths.
	d.Sources = d.Sources[1:]
	d.Sources[0].Width = 3510
	d.Sources[0].Height = 5100
	d.Sources[0].DPI = []int{150, 300, 600}
	d.Sources[0].Modes = []string{"Grayscale8"}
	if strings.Contains(d.capabilities(), "<scan:Platen>") {
		t.Fatal("invented platen")
	}
	body := strings.ReplaceAll(settings("Feeder"), ">300<", ">600<")
	if _, e := parseSettings([]byte(body), d); e != nil {
		t.Fatal(e)
	}
	if _, e := parseSettings([]byte(settings("Platen")), d); e == nil {
		t.Fatal("accepted absent source")
	}
}
func TestValidation(t *testing.T) {
	d := testDevice(t)
	base := settings("Feeder")
	for _, s := range []string{strings.ReplaceAll(base, ">300<", ">600<"), strings.Replace(base, "Grayscale8", "BlackAndWhite1", 1), strings.Replace(base, "image/jpeg", "application/pdf", 1), strings.Replace(base, "</scan:ScanSettings>", "<scan:Duplex>true</scan:Duplex></scan:ScanSettings>", 1), "<!DOCTYPE x>" + base, strings.Repeat("x", 65537), strings.Replace(base, "</scan:ScanSettings>", "<scan:ScanRegions><scan:ScanRegion><Width>100</Width><Height>100</Height><XOffset>-1</XOffset></scan:ScanRegion></scan:ScanRegions></scan:ScanSettings>", 1)} {
		if _, e := parseSettings([]byte(s), d); e == nil {
			t.Fatalf("accepted invalid %s", s[:30])
		}
	}
}
func TestStatus(t *testing.T) {
	s, a := parseStatus([]byte(`<r xmlns:n="urn:test"><n:ScannerState>Stopped</n:ScannerState><n:ScannerStateReason> MediaJam </n:ScannerStateReason><n:PaperInADF>true</n:PaperInADF></r>`))
	if s != "Stopped" || a != "ScannerAdfJam" {
		t.Fatal(s, a)
	}
}
func fakeScanner(t *testing.T, behavior string) string {
	t.Helper()
	t.Setenv("AIRSCAN_TEST_SCANNER", behavior)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "scanimage")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=TestScannerProcess -- \"$@\"\n"
	if e := os.WriteFile(p, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestScannerProcess(t *testing.T) {
	behavior := os.Getenv("AIRSCAN_TEST_SCANNER")
	if behavior == "" {
		return
	}
	pattern := ""
	for _, v := range os.Args {
		if strings.HasPrefix(v, "--batch=") {
			pattern = strings.TrimPrefix(v, "--batch=")
		}
	}
	if pattern == "" {
		os.Exit(9)
	}
	if behavior == "wait" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		fmt.Fprintln(os.Stderr, "Operation was canceled")
		os.Exit(2)
	}
	if behavior == "empty" {
		fmt.Fprintln(os.Stderr, "Document feeder out of documents")
		os.Exit(7)
	}
	for i := 1; i <= 2; i++ {
		if i == 2 && behavior == "failure" {
			fmt.Fprintln(os.Stderr, "Error during device I/O")
			os.Exit(9)
		}
		p := fmt.Sprintf(pattern, i)
		var data bytes.Buffer
		_ = jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 10)), nil)
		_ = os.WriteFile(p+".part", data.Bytes(), 0600)
		time.Sleep(20 * time.Millisecond)
		_ = os.Rename(p+".part", p)
		fmt.Println(p)
	}
	fmt.Fprintln(os.Stderr, "Document feeder out of documents")
	os.Exit(0)
}
func postJob(t *testing.T, url, source string) string {
	t.Helper()
	r, e := http.Post(url+"/eSCL/ScanJobs", "text/xml", strings.NewReader(settings(source)))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 201 {
		data, _ := io.ReadAll(r.Body)
		t.Fatal(r.StatusCode, string(data))
	}
	return r.Header.Get("Location")
}
func waitDone(t *testing.T, b *Bridge) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		b.mu.Lock()
		done := b.Active == nil
		b.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not end")
}
func TestBatchAndFailure(t *testing.T) {
	for _, behavior := range []string{"success", "failure", "empty"} {
		t.Run(behavior, func(t *testing.T) {
			b := newBridge(testDevice(t), fakeScanner(t, behavior), "")
			b.Settle = 0
			s := httptest.NewServer(b)
			defer s.Close()
			defer func() {
				b.shutdown()
				waitDone(t, b)
				b.mu.Lock()
				defer b.mu.Unlock()
				for _, j := range b.Jobs {
					os.RemoveAll(j.Dir)
				}
			}()
			url := postJob(t, s.URL, "Feeder")
			n := 2
			if behavior == "failure" {
				n = 1
			}
			if behavior == "empty" {
				n = 0
			}
			for i := 0; i < n; i++ {
				r, e := http.Get(url + "/NextDocument")
				if e != nil {
					t.Fatal(e)
				}
				if r.StatusCode != 200 {
					t.Fatal(r.StatusCode)
				}
				_, e = jpeg.Decode(r.Body)
				r.Body.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			r, e := http.Get(url + "/NextDocument")
			if e != nil {
				t.Fatal(e)
			}
			r.Body.Close()
			want := 404
			if behavior == "failure" {
				want = 500
			}
			if r.StatusCode != want {
				t.Fatal(r.StatusCode, want)
			}
			waitDone(t, b)
			b.mu.Lock()
			if len(b.Jobs) != 1 {
				t.Fatal("unexpected jobs")
			}
			for _, j := range b.Jobs {
				if j.Sent != n {
					t.Fatal(j.Sent, n)
				}
			}
			b.mu.Unlock()
		})
	}
}
func TestBusyCancelAndRestart(t *testing.T) {
	b := newBridge(testDevice(t), fakeScanner(t, "wait"), "")
	b.Settle = 0
	s := httptest.NewServer(b)
	defer s.Close()
	url := postJob(t, s.URL, "Platen")
	r, e := http.Post(s.URL+"/eSCL/ScanJobs", "text/xml", strings.NewReader(settings("Platen")))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 503 {
		t.Fatal(r.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)
	req, _ := http.NewRequest("DELETE", url, nil)
	r, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	waitDone(t, b)
	b.mu.Lock()
	for _, j := range b.Jobs {
		if j.State != "Canceled" {
			t.Fatal(j.State)
		}
		os.RemoveAll(j.Dir)
	}
	b.mu.Unlock()
	t.Setenv("AIRSCAN_TEST_SCANNER", "success")
	url = postJob(t, s.URL, "Feeder")
	r, e = http.Get(url + "/NextDocument")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	waitDone(t, b)
	b.mu.Lock()
	for _, j := range b.Jobs {
		os.RemoveAll(j.Dir)
	}
	b.mu.Unlock()
}

func TestMalformedStatusAndBrowserOrigin(t *testing.T) {
	for _, body := range []string{"", "<broken>", "<r><PaperInADF>true</PaperInADF></r>"} {
		s, a := parseStatus([]byte(body))
		if s != "Stopped" || a != "ScannerAdfUnknown" {
			t.Fatal(s, a)
		}
	}
	b := newBridge(testDevice(t), "unused", "")
	r := httptest.NewRequest("POST", "http://localhost/eSCL/ScanJobs", strings.NewReader(settings("Platen")))
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestFixedPointEdgeGeometry(t *testing.T) {
	d := testDevice(t)
	s, e := parseSettings([]byte(settings("Platen")), d)
	if e != nil {
		t.Fatal(e)
	}
	// Every top offset at the same bottom boundary must fit its SANE extent.
	for y := 0; y < 3507; y++ {
		s.Regions[0].Y = y
		s.Regions[0].Height = 3507 - y
		args := s.args(d.URI, "/tmp/job")
		var top, height float64
		for i, a := range args {
			if a == "-t" {
				top, _ = strconv.ParseFloat(args[i+1], 64)
			}
			if a == "-y" {
				height, _ = strconv.ParseFloat(args[i+1], 64)
			}
		}
		actual := int64(math.Round(top*65536)) + int64(math.Round(height*65536))
		maximum := int64(3507) * 127 * 65536 / 1500
		if actual > maximum {
			t.Fatalf("offset %d exceeds bottom: %d > %d", y, actual, maximum)
		}
	}
}

func TestConcurrentPageRequestAndDeleteCleanup(t *testing.T) {
	b := newBridge(testDevice(t), fakeScanner(t, "wait"), "")
	b.Settle = 0
	s := httptest.NewServer(b)
	defer s.Close()
	url := postJob(t, s.URL, "Platen")
	result := make(chan int, 1)
	go func() {
		r, e := http.Get(url + "/NextDocument")
		if e != nil {
			result <- 0
			return
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		result <- r.StatusCode
	}()
	deadline := time.Now().Add(5 * time.Second)
	reading := false
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, j := range b.Jobs {
			reading = j.Reading
		}
		b.mu.Unlock()
		if reading {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reading {
		t.Fatal("first NextDocument did not begin")
	}
	r, e := http.Get(url + "/NextDocument")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 409 {
		t.Fatal(r.StatusCode)
	}
	req, _ := http.NewRequest("DELETE", url, nil)
	r, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	select {
	case code := <-result:
		if code != 404 {
			t.Fatal(code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled page request stuck")
	}
	waitDone(t, b)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Jobs) != 0 {
		t.Fatal("deleted job leaked", len(b.Jobs))
	}
}

// JobInfo page counters are PWG fields, not scan-namespace fields, and count
// available pages instead of copying the client's requested batch limit.
func TestJobInfoCountersInterop(t *testing.T) {
	b := newBridge(testDevice(t), "unused", "")
	j := &Job{ID: "test", Started: time.Now(), State: "Processing", Reason: "JobScanning", Pages: []string{"one", "two"}}
	j.Settings.ImagesToTransfer = 99
	for _, sent := range []int{0, 1, 2} {
		j.Sent = sent
		body := `<scan:ScannerStatus xmlns:scan="` + scanNS + `" xmlns:pwg="` + pwgNS + `"><scan:Jobs>` + b.jobInfo(j) + `</scan:Jobs></scan:ScannerStatus>`
		var result struct {
			Jobs struct {
				Job struct {
					Remaining *int `xml:"http://www.pwg.org/schemas/2010/12/sm ImagesToTransfer"`
					Completed *int `xml:"http://www.pwg.org/schemas/2010/12/sm ImagesCompleted"`
				} `xml:"JobInfo"`
			} `xml:"Jobs"`
		}
		if err := xml.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		info := result.Jobs.Job
		if info.Remaining == nil || info.Completed == nil {
			t.Fatal("missing PWG page counters", body)
		}
		if *info.Remaining != 2-sent || *info.Completed != 2 {
			t.Fatal(body)
		}
	}
}

func TestFailureRecoveryExcludesOtherScans(t *testing.T) {
	dir := t.TempDir()
	marker, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	probe := filepath.Join(dir, "probe")
	script := "#!/bin/sh\n[ \"$1\" = --recover ] || exit 2\ntouch " + quote(marker) + "\nwhile [ ! -f " + quote(release) + " ]; do sleep 0.01; done\nprintf '%s' '<r><ScannerState>Idle</ScannerState><PaperInADF>false</PaperInADF></r>'\n"
	if e := os.WriteFile(probe, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	b := newBridge(testDevice(t), fakeScanner(t, "failure"), probe)
	b.Settle = 0
	server := httptest.NewServer(b)
	defer server.Close()
	defer func() {
		os.WriteFile(release, nil, 0600)
		b.shutdown()
		waitDone(t, b)
		for _, j := range b.Jobs {
			os.RemoveAll(j.Dir)
		}
	}()
	postJob(t, server.URL, "Feeder")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, e := http.Post(server.URL+"/eSCL/ScanJobs", "text/xml", strings.NewReader(settings("Platen")))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 503 {
		t.Fatal("scan overlapped recovery", r.StatusCode)
	}
	os.WriteFile(release, nil, 0600)
	waitDone(t, b)
	b.mu.Lock()
	if b.ScannerState != "Idle" || b.AdfState != "ScannerAdfEmpty" {
		t.Errorf("fresh post-error status lost: %s %s", b.ScannerState, b.AdfState)
	}
	for _, j := range b.Jobs {
		if j.State != "Aborted" {
			t.Errorf("failed job changed to %s", j.State)
		}
	}
	b.mu.Unlock()
}

func TestActiveADFStatusDoesNotReportOldEmpty(t *testing.T) {
	b := newBridge(testDevice(t), "unused", "")
	b.AdfState = "ScannerAdfEmpty"
	b.Active = &Job{Settings: ScanSettings{InputSource: "ADF"}}
	w := httptest.NewRecorder()
	b.status(w, httptest.NewRequest("GET", "/eSCL/ScannerStatus", nil))
	if !strings.Contains(w.Body.String(), "ScannerAdfProcessing") || strings.Contains(w.Body.String(), "ScannerAdfEmpty") {
		t.Fatal(w.Body.String())
	}
	b.Active = nil
	w = httptest.NewRecorder()
	b.status(w, httptest.NewRequest("GET", "/eSCL/ScannerStatus", nil))
	if !strings.Contains(w.Body.String(), "ScannerAdfEmpty") {
		t.Fatal(w.Body.String())
	}
}

func TestDeviceMinimumGeometry(t *testing.T) {
	d := testDevice(t)
	caps := []byte(`<r xmlns:n="urn:test"><n:PlatenMinimumSize><Width>1920</Width><Height>1920</Height></n:PlatenMinimumSize><ADFMinimumSize><Width>1000</Width><Height>2000</Height></ADFMinimumSize></r>`)
	if err := d.applyMinimums(caps); err != nil {
		t.Fatal(err)
	}
	if d.Sources[0].MinHeight != 577 || d.Sources[1].MinWidth != 301 || d.Sources[1].MinHeight != 601 {
		t.Fatal(d.Sources)
	}
	for _, h := range []int{160, 576, 577} {
		body := strings.Replace(settings("Platen"), "</scan:ScanSettings>", fmt.Sprintf(`<ScanRegions><ScanRegion><Width>1000</Width><Height>%d</Height></ScanRegion></ScanRegions></scan:ScanSettings>`, h), 1)
		_, err := parseSettings([]byte(body), d)
		if (err == nil) != (h == 577) {
			t.Fatal(h, err)
		}
	}
	if !strings.Contains(d.capabilities(), "<scan:MinHeight>577</scan:MinHeight>") {
		t.Fatal(d.capabilities())
	}
	for _, bad := range []string{`<r/>`, `<r><PlatenMinimumSize><Width>-1</Width><Height>1</Height></PlatenMinimumSize></r>`, `<broken>`} {
		if d.applyMinimums([]byte(bad)) == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestPendingPageReturnsTemporaryBusy(t *testing.T) {
	b := newBridge(testDevice(t), fakeScanner(t, "wait"), "")
	b.Settle = 0
	server := httptest.NewServer(b)
	defer server.Close()
	defer func() {
		b.shutdown()
		waitDone(t, b)
		for _, j := range b.Jobs {
			os.RemoveAll(j.Dir)
		}
	}()
	url := postJob(t, server.URL, "Platen")
	client := http.Client{Timeout: 4 * time.Second}
	r, err := client.Get(url + "/NextDocument")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 503 || r.Header.Get("Retry-After") != "1" {
		t.Fatal(r.StatusCode, r.Header)
	}
	b.mu.Lock()
	if b.Active == nil {
		t.Error("temporary page wait ended acquisition")
	}
	b.mu.Unlock()
	req, _ := http.NewRequest("DELETE", url, nil)
	r, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	waitDone(t, b)
}
