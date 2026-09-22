package main

// WS-Scan transport is independent of HPLIP. Only the local eSCL queue and
// document delivery are shared with the USB backend.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const wsdNS = "http://schemas.microsoft.com/windows/2006/08/wdp/scan"
const wsdControlLimit = 2 << 20
const wsdImageLimit = 192 << 20

// Keep the LaunchAgent alive while macOS requests Local Network permission.
// These retries only read capabilities; scan creation is never retried here.
func waitWSDDevice(ctx context.Context, fetch func(context.Context) (Device, error), delay time.Duration) (Device, error) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		d, err := fetch(attempt)
		cancel()
		if err == nil {
			return d, nil
		}
		if ctx.Err() != nil {
			break
		}
		log.Printf("WSD startup: %v; retrying in %s. Check printer connectivity and macOS Local Network permission for HP AirScan Bridge.", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return Device{}, ctx.Err()
}

type wsdNode struct {
	Name     xml.Name
	Text     string
	Children []*wsdNode
}

func parseWSD(data []byte) (*wsdNode, error) {
	if len(data) > wsdControlLimit {
		return nil, fmt.Errorf("WSD XML too large")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	root := &wsdNode{}
	stack := []*wsdNode{root}
	count := 0
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch v := t.(type) {
		case xml.Directive:
			return nil, fmt.Errorf("WSD XML directives forbidden")
		case xml.StartElement:
			count++
			if count > 30000 || len(stack) > 64 {
				return nil, fmt.Errorf("WSD XML nesting/element limit")
			}
			n := &wsdNode{Name: v.Name}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].Text += string(v)
		}
	}
	if len(root.Children) != 1 || root.Children[0].Name.Local != "Envelope" {
		return nil, fmt.Errorf("not a SOAP envelope")
	}
	return root, nil
}
func (n *wsdNode) find(name string) *wsdNode {
	if n == nil {
		return nil
	}
	if n.Name.Local == name {
		return n
	}
	for _, c := range n.Children {
		if v := c.find(name); v != nil {
			return v
		}
	}
	return nil
}
func (n *wsdNode) value(name string) string {
	if v := n.find(name); v != nil {
		return strings.TrimSpace(v.Text)
	}
	return ""
}
func (n *wsdNode) values(name string) []string {
	if n == nil {
		return nil
	}
	var r []string
	if n.Name.Local == name {
		r = append(r, strings.TrimSpace(n.Text))
	}
	for _, c := range n.Children {
		r = append(r, c.values(name)...)
	}
	return r
}

type wsdFault string

func (e wsdFault) Error() string { return string(e) }
func noWSDImages(e error) bool {
	var f wsdFault
	return errors.As(e, &f) && (f == "ClientErrorNoImagesAvailable" || f == "ClientErrorNoDocuments")
}

type WSDClient struct {
	URL  string
	HTTP *http.Client
}

func newWSDClient(raw string) (*WSDClient, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	if u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return nil, fmt.Errorf("AIRSCAN_WSD_URL must be an explicit http://host:port/scanner endpoint")
	}
	return &WSDClient{URL: u.String(), HTTP: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 90 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true}, Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("WSD redirects forbidden") }}}, nil
}
func (c *WSDClient) call(ctx context.Context, action, body string, limit int64) ([]byte, string, error) {
	start := time.Now()
	data, ct, e := c.request(ctx, action, body, limit)
	if os.Getenv("AIRSCAN_DEBUG") == "1" {
		log.Printf("WSD %s: bytes=%d elapsed=%s error=%v", action, len(data), time.Since(start).Round(time.Millisecond), e)
	}
	if e != nil {
		e = fmt.Errorf("%s: %w", action, e)
	}
	return data, ct, e
}
func (c *WSDClient) request(ctx context.Context, action, body string, limit int64) ([]byte, string, error) {
	id := make([]byte, 16)
	if _, e := rand.Read(id); e != nil {
		return nil, "", e
	}
	envelope := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:sca="` + wsdNS + `"><s:Header><a:MessageID>urn:uuid:` + fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]) + `</a:MessageID><a:To>` + xmlText(c.URL) + `</a:To><a:ReplyTo><a:Address>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:Address></a:ReplyTo><a:Action>` + wsdNS + `/` + action + `</a:Action></s:Header><s:Body>` + body + `</s:Body></s:Envelope>`
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, strings.NewReader(envelope))
	if e != nil {
		return nil, "", e
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	req.Header.Set("User-Agent", "WSDAPI")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return nil, "", e
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("WSD response too large")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/") {
		n, err := parseWSD(data)
		if err != nil {
			return nil, "", fmt.Errorf("HTTP %d: %w", resp.StatusCode, err)
		}
		if f := n.find("Fault"); f != nil {
			code := f.find("Subcode").value("Value")
			if i := strings.LastIndex(code, ":"); i >= 0 {
				code = code[i+1:]
			}
			if code == "" {
				code = "UnknownSOAPFault"
			}
			return nil, "", wsdFault(code)
		}
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("WSD HTTP %d", resp.StatusCode)
	}
	return data, ct, nil
}
func (c *WSDClient) elements(ctx context.Context, names ...string) (*wsdNode, error) {
	var b strings.Builder
	b.WriteString("<sca:GetScannerElementsRequest><sca:RequestedElements>")
	for _, n := range names {
		fmt.Fprintf(&b, "<sca:Name>sca:%s</sca:Name>", n)
	}
	b.WriteString("</sca:RequestedElements></sca:GetScannerElementsRequest>")
	data, _, e := c.call(ctx, "GetScannerElements", b.String(), wsdControlLimit)
	if e != nil {
		return nil, e
	}
	return parseWSD(data)
}
func (c *WSDClient) device(ctx context.Context) (Device, error) {
	d := Device{URI: c.URL, Model: "HP WSD scanner", Transport: "WSD LAN"}
	n, e := c.elements(ctx, "ScannerConfiguration", "ScannerDescription")
	if e != nil {
		return d, e
	}
	if v := n.value("ScannerName"); v != "" {
		d.Model = v
	}
	cfg := n.find("ScannerConfiguration")
	if cfg == nil {
		return d, fmt.Errorf("missing WSD configuration")
	}
	dib := false
	for _, v := range cfg.values("FormatValue") {
		if v == "dib" {
			dib = true
		}
	}
	if !dib {
		return d, fmt.Errorf("WSD scanner must support uncompressed dib")
	}
	for _, spec := range []struct{ node, prefix, name string }{{"Platen", "Platen", "Flatbed"}, {"ADFFront", "ADF", "ADF"}} {
		v := cfg.find(spec.node)
		if v == nil {
			continue
		}
		s := Source{Name: spec.name}
		size := func(kind, axis string) (int, error) {
			num, err := strconv.Atoi(v.find(spec.prefix + kind + "Size").value(axis))
			if err != nil || num < 1 || num > 20000 {
				return 0, fmt.Errorf("invalid WSD source size")
			}
			if kind == "Minimum" {
				return (num*3 + 9) / 10, nil
			}
			return num * 3 / 10, nil
		}
		if s.MinWidth, e = size("Minimum", "Width"); e != nil {
			return d, e
		}
		if s.MinHeight, e = size("Minimum", "Height"); e != nil {
			return d, e
		}
		if s.Width, e = size("Maximum", "Width"); e != nil {
			return d, e
		}
		if s.Height, e = size("Maximum", "Height"); e != nil {
			return d, e
		}
		if s.MinWidth > s.Width || s.MinHeight > s.Height {
			return d, fmt.Errorf("invalid WSD source bounds")
		}
		s.DefaultHeight = min(s.Height, 3507)
		res := v.find(spec.prefix + "Resolutions")
		widths := res.find("Widths").values("Width")
		heights := res.find("Heights").values("Height")
		for _, dpi := range []int{75, 150, 300, 600} {
			hasW, hasH := false, false
			for _, a := range widths {
				hasW = hasW || a == strconv.Itoa(dpi)
			}
			for _, a := range heights {
				hasH = hasH || a == strconv.Itoa(dpi)
			}
			if hasW && hasH {
				s.DPI = append(s.DPI, dpi)
			}
		}
		for _, mode := range v.find(spec.prefix + "Color").values("ColorEntry") {
			if mode == "RGB24" || mode == "Grayscale8" {
				s.Modes = append(s.Modes, mode)
			}
		}
		if len(s.DPI) == 0 || len(s.Modes) == 0 {
			return d, fmt.Errorf("unsupported WSD source modes/resolutions")
		}
		d.Sources = append(d.Sources, s)
	}
	if len(d.Sources) == 0 {
		return d, fmt.Errorf("no supported WSD sources")
	}
	return d, nil
}
func (c *WSDClient) status(ctx context.Context) (string, string, error) {
	n, e := c.elements(ctx, "ScannerStatus")
	if e != nil {
		return "Stopped", "ScannerAdfUnknown", e
	}
	state := n.value("ScannerState")
	adf := "ScannerAdfUnknown"
	if state != "Idle" && state != "Processing" {
		state = "Stopped"
	}
	for _, v := range n.values("ScannerStateReason") {
		if v == "MediaJam" {
			adf = "ScannerAdfJam"
			state = "Stopped"
		}
	}
	return state, adf, nil
}
func (c *WSDClient) activeJobs(ctx context.Context) (*wsdNode, error) {
	d, _, e := c.call(ctx, "GetActiveJobs", "<sca:GetActiveJobsRequest/>", wsdControlLimit)
	if e != nil {
		return nil, e
	}
	return parseWSD(d)
}
func (c *WSDClient) cancelJob(ctx context.Context, id string) error {
	_, _, e := c.call(ctx, "CancelJob", "<sca:CancelJobRequest><sca:JobId>"+xmlText(id)+"</sca:JobId></sca:CancelJobRequest>", wsdControlLimit)
	return e
}
func wsdTicket(s ScanSettings, name string) string {
	r := s.Regions[0]
	source := "Platen"
	count := 1
	if s.InputSource == "ADF" {
		source = "ADF"
		count = s.ImagesToTransfer
	}
	// WS-Scan uses thousandths of an inch; eSCL uses 1/300 inch. Floor
	// components separately so an offset plus size never exceeds source bounds.
	return fmt.Sprintf(`<sca:CreateScanJobRequest><sca:ScanTicket><sca:JobDescription><sca:JobName>%s</sca:JobName><sca:JobOriginatingUserName>hp-scan-macos</sca:JobOriginatingUserName><sca:JobInformation>Local AirScan bridge</sca:JobInformation></sca:JobDescription><sca:DocumentParameters><sca:Format>dib</sca:Format><sca:ImagesToTransfer>%d</sca:ImagesToTransfer><sca:ContentType>Auto</sca:ContentType><sca:InputSize><sca:InputMediaSize><sca:Width>%d</sca:Width><sca:Height>%d</sca:Height></sca:InputMediaSize></sca:InputSize><sca:InputSource>%s</sca:InputSource><sca:MediaSides><sca:MediaFront><sca:ColorProcessing>%s</sca:ColorProcessing><sca:Resolution><sca:Width>%d</sca:Width><sca:Height>%d</sca:Height></sca:Resolution><sca:ScanRegion><sca:ScanRegionXOffset>%d</sca:ScanRegionXOffset><sca:ScanRegionYOffset>%d</sca:ScanRegionYOffset><sca:ScanRegionWidth>%d</sca:ScanRegionWidth><sca:ScanRegionHeight>%d</sca:ScanRegionHeight></sca:ScanRegion></sca:MediaFront></sca:MediaSides></sca:DocumentParameters></sca:ScanTicket></sca:CreateScanJobRequest>`, xmlText(name), count, r.Width*10/3, r.Height*10/3, source, s.ColorMode, s.XResolution, s.YResolution, r.X*10/3, r.Y*10/3, r.Width*10/3, r.Height*10/3)
}
func wsdBitmap(data []byte, ct string) ([]byte, error) {
	typ, params, e := mime.ParseMediaType(ct)
	if e != nil || typ != "multipart/related" || params["boundary"] == "" {
		return nil, fmt.Errorf("expected WSD multipart image")
	}
	mr := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	var bitmap []byte
	soapSeen := false
	count := 0
	for {
		p, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		count++
		if count > 2 {
			return nil, fmt.Errorf("unexpected WSD parts")
		}
		body, e := io.ReadAll(p)
		if e != nil {
			return nil, e
		}
		t, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		switch t {
		case "application/xop+xml":
			n, e := parseWSD(body)
			if e != nil || n.find("RetrieveImageResponse") == nil {
				return nil, fmt.Errorf("invalid WSD image envelope")
			}
			soapSeen = true
		case "image/bmp", "application/octet-stream":
			if bitmap != nil {
				return nil, fmt.Errorf("duplicate WSD image")
			}
			bitmap = body
		default:
			return nil, fmt.Errorf("unsupported WSD part type %s", t)
		}
	}
	if !soapSeen || len(bitmap) == 0 {
		return nil, fmt.Errorf("missing WSD image")
	}
	return bitmap, nil
}

// BI_RGB BMP/DIB only. Reject compressed/bitfield layouts before allocating.
func decodeWSDBMP(data []byte) (*image.RGBA, error) {
	start, offset := 0, 0
	if len(data) >= 14 && string(data[:2]) == "BM" {
		start = 14
		offset = int(binary.LittleEndian.Uint32(data[10:14]))
	}
	if len(data) < start+40 {
		return nil, fmt.Errorf("short DIB")
	}
	d := data[start:]
	header := int(binary.LittleEndian.Uint32(d))
	w := int(int32(binary.LittleEndian.Uint32(d[4:])))
	signedH := int(int32(binary.LittleEndian.Uint32(d[8:])))
	h := signedH
	if h < 0 {
		h = -h
	}
	bits := int(binary.LittleEndian.Uint16(d[14:]))
	colors := int(binary.LittleEndian.Uint32(d[32:]))
	compression := binary.LittleEndian.Uint32(d[16:])
	if header < 40 || header > 124 || len(d) < header || w < 1 || h < 1 || int64(w)*int64(h) > 40000000 || binary.LittleEndian.Uint16(d[12:]) != 1 || compression != 0 || (bits != 8 && bits != 24 && bits != 32) {
		return nil, fmt.Errorf("unsupported DIB layout")
	}
	if bits == 8 {
		if colors == 0 {
			colors = 256
		}
		if colors > 256 {
			return nil, fmt.Errorf("invalid DIB palette")
		}
	} else if colors != 0 {
		return nil, fmt.Errorf("unexpected DIB palette")
	}
	paletteEnd := start + header + colors*4
	if offset == 0 {
		offset = paletteEnd
	}
	stride := (w*bits + 31) / 32 * 4
	if offset < paletteEnd || offset > len(data) || int64(stride)*int64(h) > int64(len(data)-offset) {
		return nil, fmt.Errorf("truncated DIB raster")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := y
		if signedH > 0 {
			sy = h - 1 - y
		}
		row := data[offset+sy*stride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			p := row[x*(bits/8):]
			if bits == 8 {
				idx := int(p[0])
				if idx >= colors {
					return nil, fmt.Errorf("palette index out of range")
				}
				p = data[start+header+idx*4:]
			}
			dst[x*4] = p[2]
			dst[x*4+1] = p[1]
			dst[x*4+2] = p[0]
			dst[x*4+3] = 255
		}
	}
	return img, nil
}
func (b *Bridge) runWSD(j *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-j.Cancel:
			cancel()
		case <-done:
		case <-ctx.Done():
		}
	}()
	c := b.WSD
	jobID := ""
	jobName := "hp-airscan-" + j.ID
	var err error
	detail := ""
	pages := 0
	var total int
	// Never overlap another client's WSD job, even if the scanner says Idle.
	state, _, e := c.status(ctx)
	if e != nil || state != "Idle" {
		b.finish(j, fmt.Errorf("WSD scanner unavailable: %s (%v)", state, e), "")
		return
	}
	active, e := c.activeJobs(ctx)
	if e != nil || len(active.values("JobId")) != 0 {
		b.finish(j, fmt.Errorf("WSD scanner busy (%v)", e), "")
		return
	}
	b.mu.Lock()
	if j.State != "Canceled" {
		j.State = "Processing"
		j.Reason = "JobScanning"
		b.notify(j)
	}
	b.mu.Unlock()
	data, _, err := c.call(ctx, "CreateScanJob", wsdTicket(j.Settings, jobName), wsdControlLimit)
	if err == nil {
		var n *wsdNode
		n, err = parseWSD(data)
		if err == nil {
			jobID = n.value("JobId")
			token := n.value("JobToken")
			if jobID == "" || token == "" {
				err = fmt.Errorf("WSD job identity missing")
			} else {
				count := j.Settings.ImagesToTransfer
				if j.Settings.InputSource == "Flatbed" {
					count = 1
				}
				for pages < 100 && (count == 0 || pages < count) {
					if ctx.Err() != nil {
						err = ctx.Err()
						break
					}
					body := `<sca:RetrieveImageRequest><sca:DocumentDescription><sca:DocumentName>scan.bmp</sca:DocumentName></sca:DocumentDescription><sca:JobId>` + xmlText(jobID) + `</sca:JobId><sca:JobToken>` + xmlText(token) + `</sca:JobToken></sca:RetrieveImageRequest>`
					var ct string
					data, ct, err = c.call(ctx, "RetrieveImage", body, wsdImageLimit)
					if noWSDImages(err) && j.Settings.InputSource == "ADF" {
						detail = "out of documents"
						if pages > 0 {
							err = nil
						}
						break
					}
					if err != nil {
						break
					}
					var raw []byte
					raw, err = wsdBitmap(data, ct)
					if err != nil {
						break
					}
					var img *image.RGBA
					img, err = decodeWSDBMP(raw)
					if err != nil {
						break
					}
					bounds, cropErr := wsdPageBounds(img.Bounds(), j.Settings)
					if cropErr != nil {
						err = cropErr
						break
					}
					if os.Getenv("AIRSCAN_DEBUG") == "1" {
						log.Printf("WSD raster %dx%d -> %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), bounds.Dx(), bounds.Dy())
					}

					var encoded bytes.Buffer
					err = jpeg.Encode(&encoded, img.SubImage(bounds), &jpeg.Options{Quality: 95})
					if err != nil {
						break
					}
					total += encoded.Len()
					if encoded.Len() > 32<<20 || total > 256<<20 {
						err = fmt.Errorf("WSD spool limit exceeded")
						break
					}
					if ctx.Err() != nil {
						err = ctx.Err()
						break
					}
					name := filepath.Join(j.Dir, fmt.Sprintf("page-%04d.jpg", pages+1))
					err = os.WriteFile(name, encoded.Bytes(), 0600)
					if err != nil {
						break
					}
					b.mu.Lock()
					if j.State != "Canceled" {
						j.Pages = append(j.Pages, name)
						b.notify(j)
					}
					b.mu.Unlock()
					pages++
				}
				if pages == 100 && count == 0 {
					err = fmt.Errorf("WSD page limit reached")
				}
			}
		}
	}
	// Cleanup uses a separate deadline; cancellation of the HTTP read must not
	// cancel CancelJob too. An ambiguous create is not replayed: locate only our
	// uniquely named job, never cancel another client's work.
	cleanup, stop := context.WithTimeout(context.Background(), 40*time.Second)
	defer stop()
	if jobID == "" {
		if a, e := c.activeJobs(cleanup); e == nil {
			for _, summary := range a.find("ActiveJobs").children() {
				if summary.value("JobName") == jobName {
					jobID = summary.value("JobId")
					break
				}
			}
		}
	}
	if jobID != "" {
		if e := c.cancelJob(cleanup, jobID); e != nil {
			err = errors.Join(err, fmt.Errorf("WSD cleanup: %w", e))
		}
	}
	state, adf, e := c.status(cleanup)
	if e == nil {
		b.mu.Lock()
		j.RecoveryState, j.RecoveryADF = state, adf
		b.mu.Unlock()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		b.mu.Lock()
		j.Failure = "JobTimedOut"
		b.mu.Unlock()
	}
	b.finish(j, err, detail)
}
func (n *wsdNode) children() []*wsdNode {
	if n == nil {
		return nil
	}
	return n.Children
}

// Firmware may round a sensor raster in either direction (M127fn returns
// 2528 columns for a 2550-column request at 300 DPI). Preserve available pixels;
// trim excess columns without stretching or inventing missing image content.
func wsdPageBounds(actual image.Rectangle, s ScanSettings) (image.Rectangle, error) {
	r := s.Regions[0]
	w := r.Width * s.XResolution / 300
	h := r.Height * s.YResolution / 300
	if w < 1 || h < 1 || actual.Dx() < max(1, w-63) || actual.Dx() > w+63 || actual.Dy() < 1 || actual.Dy() > h+8 || (s.InputSource == "Flatbed" && actual.Dy() < max(1, h-8)) {
		return image.Rectangle{}, fmt.Errorf("WSD raster %dx%d does not match requested %dx%d", actual.Dx(), actual.Dy(), w, h)
	}
	return image.Rect(0, 0, min(w, actual.Dx()), min(h, actual.Dy())), nil
}
