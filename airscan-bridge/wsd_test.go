package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWSDStartupPermissionRecovery(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := waitWSDDevice(ctx, func(attempt context.Context) (Device, error) {
		if _, ok := attempt.Deadline(); !ok {
			t.Fatal("capability request must have a deadline")
		}
		calls++
		if calls == 1 {
			return Device{}, fmt.Errorf("local network denied")
		}
		return Device{Model: "Scanner"}, nil
	}, time.Millisecond)
	if err != nil || calls != 2 || d.Model != "Scanner" {
		t.Fatalf("recovery: device=%+v calls=%d err=%v", d, calls, err)
	}
}

func TestWSDStartupStopsDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := waitWSDDevice(ctx, func(context.Context) (Device, error) {
			close(started)
			return Device{}, fmt.Errorf("printer offline")
		}, time.Hour)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup did not stop promptly")
	}
}

func soap(body string) string {
	return `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:w="` + wsdNS + `"><s:Body>` + body + `</s:Body></s:Envelope>`
}
func fault(code string) string {
	return soap(`<s:Fault><s:Code><s:Subcode><s:Value>w:` + code + `</s:Value></s:Subcode></s:Code></s:Fault>`)
}
func bmpTest(bits int, topDown bool) []byte {
	w, h := 8, 2
	stride := (w*bits + 31) / 32 * 4
	palette := 0
	if bits == 8 {
		palette = 256 * 4
	}
	d := make([]byte, 54+palette+stride*h)
	copy(d, "BM")
	binary.LittleEndian.PutUint32(d[2:], uint32(len(d)))
	binary.LittleEndian.PutUint32(d[10:], uint32(54+palette))
	binary.LittleEndian.PutUint32(d[14:], 40)
	binary.LittleEndian.PutUint32(d[18:], uint32(w))
	hh := int32(h)
	if topDown {
		hh = -hh
	}
	binary.LittleEndian.PutUint32(d[22:], uint32(hh))
	binary.LittleEndian.PutUint16(d[26:], 1)
	binary.LittleEndian.PutUint16(d[28:], uint16(bits))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := 54 + palette + y*stride + x*(bits/8)
			if bits == 8 {
				d[i] = 1
				d[54+4+2] = 255
			} else {
				d[i+2] = 255
			}
		}
	}
	return d
}
func multipartImage(d []byte) ([]byte, string) {
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	h := textproto.MIMEHeader{"Content-Type": []string{"application/xop+xml"}}
	p, _ := m.CreatePart(h)
	io.WriteString(p, soap("<w:RetrieveImageResponse/>"))
	p, _ = m.CreatePart(textproto.MIMEHeader{"Content-Type": []string{"image/bmp"}})
	p.Write(d)
	m.Close()
	return b.Bytes(), "multipart/related; boundary=" + m.Boundary()
}
func TestWSDBitmapLayouts(t *testing.T) {
	for _, bits := range []int{8, 24, 32} {
		for _, top := range []bool{true, false} {
			d := bmpTest(bits, top)
			img, e := decodeWSDBMP(d)
			if e != nil || img.Bounds().Dx() != 8 || img.RGBAAt(0, 0).R != 255 || img.RGBAAt(0, 0).A != 255 {
				t.Fatal(bits, top, e)
			}
			d = d[:len(d)-1]
			if _, e := decodeWSDBMP(d); e == nil {
				t.Fatal("truncated BMP accepted")
			}
		}
	}
	d := bmpTest(32, true)
	binary.LittleEndian.PutUint32(d[30:], 3)
	if _, e := decodeWSDBMP(d); e == nil {
		t.Fatal("bitfields accepted")
	}
	binary.LittleEndian.PutUint32(d[30:], 0)
	binary.LittleEndian.PutUint32(d[18:], 0x7fffffff)
	if _, e := decodeWSDBMP(d); e == nil {
		t.Fatal("huge bitmap accepted")
	}
}
func TestWSDCapabilities(t *testing.T) {
	fixture, e := os.ReadFile("testdata/wsd-m127-config.xml")
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		w.Write(fixture)
	}))
	defer server.Close()
	c, _ := newWSDClient(server.URL + "/scanner")
	d, e := c.device(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Sources) != 2 || d.Sources[0].Height != 3300 || d.Sources[1].Height != 4500 || d.Sources[1].DefaultHeight != 3507 {
		t.Fatal(d)
	}
	if strings.Contains(d.capabilities(), ">1200<") || strings.Contains(d.capabilities(), "BlackAndWhite1") {
		t.Fatal("unsupported modes advertised")
	}
	fixture = bytes.ReplaceAll(fixture, []byte(">dib<"), []byte(">hpraw<"))
	if _, e = c.device(context.Background()); e == nil {
		t.Fatal("HPRAW accepted")
	}
}
func TestWSDTicketAndBounds(t *testing.T) {
	s := ScanSettings{InputSource: "ADF", XResolution: 300, YResolution: 300, ColorMode: "RGB24", Regions: []Region{{X: 118, Y: 236, Width: 1181, Height: 1771}}}
	v := wsdTicket(s, "a<&")
	for _, want := range []string{"<sca:ImagesToTransfer>0", "<sca:ScanRegionXOffset>393", "<sca:ScanRegionYOffset>786", "<sca:ScanRegionWidth>3936", "<sca:ScanRegionHeight>5903", "a&lt;&amp;"} {
		if !strings.Contains(v, want) {
			t.Fatal(want, v)
		}
	}
	for _, u := range []string{"file:///tmp/x", "http://user:pass@host/scanner", "http://host/scanner?q=1", "https://host/scanner"} {
		if _, e := newWSDClient(u); e == nil {
			t.Fatal(u)
		}
	}
	if _, e := parseWSD([]byte("<!DOCTYPE x>" + soap(""))); e == nil {
		t.Fatal("DTD accepted")
	}
	if _, e := parseWSD([]byte(soap(strings.Repeat("<x>", 70) + strings.Repeat("</x>", 70)))); e == nil {
		t.Fatal("deep XML accepted")
	}
}

type wsdMock struct {
	mu                       sync.Mutex
	mode                     string
	created, canceled, reads int
	jobName                  string
	entered                  chan struct{}
}

func (m *wsdMock) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	n, e := parseWSD(data)
	if e != nil {
		http.Error(w, "bad XML", 400)
		return
	}
	action := n.value("Action")
	action = action[strings.LastIndex(action, "/")+1:]
	w.Header().Set("Content-Type", "application/soap+xml")
	m.mu.Lock()
	defer m.mu.Unlock()
	switch action {
	case "GetScannerElements":
		io.WriteString(w, soap("<w:ScannerStatus><w:ScannerState>Idle</w:ScannerState></w:ScannerStatus>"))
	case "GetActiveJobs":
		jobs := ""
		if m.mode == "ambiguous" && m.created > 0 && m.canceled == 0 {
			jobs = "<w:JobSummary><w:JobId>7</w:JobId><w:JobName>" + m.jobName + "</w:JobName></w:JobSummary>"
		}
		if m.mode == "busy" {
			jobs = "<w:JobSummary><w:JobId>99</w:JobId><w:JobName>other client</w:JobName></w:JobSummary>"
		}
		io.WriteString(w, soap("<w:ActiveJobs>"+jobs+"</w:ActiveJobs>"))
	case "CreateScanJob":
		m.created++
		m.jobName = n.value("JobName")
		if m.mode == "ambiguous" {
			http.Error(w, "lost response", 500)
			return
		}
		io.WriteString(w, soap("<w:CreateScanJobResponse><w:JobId>7</w:JobId><w:JobToken>token</w:JobToken></w:CreateScanJobResponse>"))
	case "RetrieveImage":
		m.reads++
		if m.mode == "cancel" {
			close(m.entered)
			m.mu.Unlock()
			<-r.Context().Done()
			m.mu.Lock()
			return
		}
		if m.mode == "empty" || m.reads > 2 {
			w.WriteHeader(400)
			io.WriteString(w, fault("ClientErrorNoImagesAvailable"))
			return
		}
		if m.mode == "partial" && m.reads == 2 {
			w.WriteHeader(500)
			io.WriteString(w, fault("ServerErrorInternalError"))
			return
		}
		d, ct := multipartImage(bmpTest(32, true))
		w.Header().Set("Content-Type", ct)
		w.Write(d)
	case "CancelJob":
		if n.value("JobId") != "7" {
			panic("canceling someone else's job")
		}
		m.canceled++
		io.WriteString(w, soap("<w:CancelJobResponse/>"))
	default:
		http.Error(w, "unknown action", 400)
	}
}
func TestWSDLifecycle(t *testing.T) {
	for _, mode := range []string{"batch", "empty", "partial", "cancel", "ambiguous", "busy"} {
		t.Run(mode, func(t *testing.T) {
			m := &wsdMock{mode: mode, entered: make(chan struct{})}
			server := httptest.NewServer(http.HandlerFunc(m.serve))
			defer server.Close()
			c, _ := newWSDClient(server.URL + "/scanner")
			b := newBridge(Device{}, "", "")
			b.WSD = c
			b.Settle = 0
			j := &Job{ID: "test", Dir: t.TempDir(), Settings: ScanSettings{InputSource: "ADF", XResolution: 300, YResolution: 300, ColorMode: "RGB24", Regions: []Region{{Width: 2, Height: 2}}}, State: "Pending", Cancel: make(chan struct{}), Changed: make(chan struct{})}
			b.Jobs[j.ID] = j
			b.Active = j
			done := make(chan struct{})
			go func() { b.runWSD(j); close(done) }()
			if mode == "cancel" {
				select {
				case <-m.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("no retrieval")
				}
				b.mu.Lock()
				b.cancel(j)
				b.mu.Unlock()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("worker stuck")
			}
			if !j.Done || b.Active != nil {
				t.Fatal("worker retained")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			want := 2
			if mode == "partial" {
				want = 1
			}
			if mode == "empty" || mode == "cancel" || mode == "ambiguous" || mode == "busy" {
				want = 0
			}
			if len(j.Pages) != want {
				t.Fatal(mode, len(j.Pages), j)
			}
			if mode == "busy" {
				if m.created != 0 || m.canceled != 0 {
					t.Fatal("touched external job")
				}
			} else if m.created != 1 || m.canceled != 1 {
				t.Fatal("creation replayed or cleanup missed", m.created, m.canceled)
			}
			if mode == "cancel" && j.State != "Canceled" {
				t.Fatal(j.State)
			}
			if mode == "empty" && j.ErrorHTTP != 404 {
				t.Fatal(j.ErrorHTTP)
			}
			if mode == "partial" && j.ErrorHTTP != 500 {
				t.Fatal(j.ErrorHTTP)
			}
			for _, p := range j.Pages {
				f, _ := os.Open(p)
				img, e := jpeg.Decode(f)
				f.Close()
				if e != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
					t.Fatal("conversion/crop", e)
				}
			}
		})
	}
}
func TestWSDHTTPGuards(t *testing.T) {
	for _, mode := range []string{"limit", "redirect", "fault"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "limit":
					fmt.Fprint(w, strings.Repeat("x", 100))
				case "redirect":
					http.Redirect(w, r, "http://example.invalid", 302)
				case "fault":
					w.WriteHeader(400)
					fmt.Fprint(w, fault("ClientErrorNoImagesAvailable"))
				}
			}))
			defer s.Close()
			c, _ := newWSDClient(s.URL + "/scanner")
			limit := int64(20)
			if mode == "fault" {
				limit = wsdControlLimit
			}
			_, _, e := c.call(context.Background(), "RetrieveImage", "", limit)
			if e == nil {
				t.Fatal(mode)
			}
			if mode == "fault" && !noWSDImages(e) {
				t.Fatal(e)
			}
		})
	}
}

func TestWSDHardwareRasterRounding(t *testing.T) {
	s := ScanSettings{InputSource: "Flatbed", XResolution: 300, YResolution: 300, Regions: []Region{{Width: 2550, Height: 3300}}}
	for _, tc := range []struct {
		width, height, want int
		ok                  bool
	}{{2528, 3300, 2528, true}, {2560, 3300, 2550, true}, {2400, 3300, 0, false}, {2550, 100, 0, false}, {2550, 3400, 0, false}} {
		b, e := wsdPageBounds(image.Rect(0, 0, tc.width, tc.height), s)
		if (e == nil) != tc.ok || (e == nil && b.Dx() != tc.want) {
			t.Fatal(tc, b, e)
		}
	}
}
