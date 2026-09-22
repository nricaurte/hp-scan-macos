package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const scanNS = "http://schemas.hp.com/imaging/escl/2011/05/03"
const pwgNS = "http://www.pwg.org/schemas/2010/12/sm"

type Source struct {
	Name                         string
	Width, Height, DefaultHeight int
	MinWidth, MinHeight          int
	DPI                          []int
	Modes                        []string
}
type Device struct {
	URI, Model string
	Transport  string
	Sources    []Source
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func stableUUID(s string) string {
	h := sha256.Sum256([]byte(s))
	h[6] = (h[6] & 15) | 80
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func deviceModel(uri string) string {
	s := strings.Split(strings.TrimPrefix(uri, "hpaio:"), "?")[0]
	p := strings.LastIndex(s, "/")
	return strings.ReplaceAll(s[p+1:], "_", " ")
}
func commandEnv() []string {
	var env []string
	for _, s := range os.Environ() {
		if !strings.HasPrefix(s, "LC_") && !strings.HasPrefix(s, "LANG=") && !strings.HasPrefix(s, "SANE_DEBUG_") && !strings.HasPrefix(s, "DYLD_") && !strings.HasPrefix(s, "HPAIO_SOAPHT_PLUGIN=") && !strings.HasPrefix(s, "SANE_CONFIG_DIR=") {
			env = append(env, s)
		}
	}
	return append(env, "LC_ALL=C", "LANG=C")
}
func outputCommand(path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = commandEnv()
	return cmd.Output()
}
func option(text, key string) string {
	re := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(key) + `\s+([^\r\n]+)`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
func parseSource(name, text string) (Source, error) {
	s := Source{Name: name, MinWidth: 1, MinHeight: 1}
	modes := strings.Fields(option(text, "--mode"))
	if len(modes) > 0 {
		for _, m := range strings.Split(modes[0], "|") {
			if m == "Gray" {
				s.Modes = append(s.Modes, "Grayscale8")
			}
			if m == "Color" {
				s.Modes = append(s.Modes, "RGB24")
			}
		}
	}
	res := strings.Fields(option(text, "--resolution"))
	if len(res) > 0 {
		for _, v := range strings.Split(strings.TrimSuffix(res[0], "dpi"), "|") {
			n, e := strconv.Atoi(v)
			if e == nil && n > 0 && n <= 1200 {
				s.DPI = append(s.DPI, n)
			}
		}
	}
	geometry := regexp.MustCompile(`^0\.\.([0-9.]+)mm\s+\[([0-9.]+)\]`)
	for _, k := range []string{"-x", "-y"} {
		m := geometry.FindStringSubmatch(option(text, k))
		if len(m) != 3 {
			return s, fmt.Errorf("unsupported %s geometry for %s", k, name)
		}
		max, e := strconv.ParseFloat(m[1], 64)
		def, e2 := strconv.ParseFloat(m[2], 64)
		if e != nil || e2 != nil || max <= 0 || max > 508 || def <= 0 || def > max {
			return s, fmt.Errorf("invalid geometry")
		}
		units := int(math.Floor(max*300/25.4 + 0.001))
		if k == "-x" {
			s.Width = units
		} else {
			s.Height = units
			s.DefaultHeight = int(math.Floor(def*300/25.4 + 0.001))
		}
	}
	if len(s.Modes) == 0 || len(s.DPI) == 0 {
		return s, fmt.Errorf("unsupported modes/resolutions for %s", name)
	}
	return s, nil
}
func detectDevice(scanimage, uri string) (Device, error) {
	d := Device{URI: uri}
	if uri == "" {
		out, e := outputCommand(scanimage, "-f", "%d%n")
		if e != nil {
			return d, e
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "hpaio:/usb/") {
				if d.URI != "" {
					return d, fmt.Errorf("multiple HP USB scanners: set AIRSCAN_DEVICE")
				}
				d.URI = line
			}
		}
	}
	if !strings.HasPrefix(d.URI, "hpaio:/usb/") {
		return d, fmt.Errorf("select a local hpaio:/usb/ scanner with AIRSCAN_DEVICE")
	}
	d.Model = deviceModel(d.URI)
	out, e := outputCommand(scanimage, "-d", d.URI, "-A")
	if e != nil {
		return d, e
	}
	fields := strings.Fields(option(string(out), "--source"))
	if len(fields) == 0 {
		return d, fmt.Errorf("scanner exposes no sources")
	}
	for _, name := range strings.Split(fields[0], "|") {
		if name != "Flatbed" && name != "ADF" {
			continue
		}
		o, e := outputCommand(scanimage, "-d", d.URI, "--source", name, "-A")
		if e != nil {
			return d, e
		}
		s, e := parseSource(name, string(o))
		if e != nil {
			return d, e
		}
		d.Sources = append(d.Sources, s)
	}
	if len(d.Sources) == 0 {
		return d, fmt.Errorf("no implemented sources")
	}
	return d, nil
}
func (d Device) source(name string) (Source, bool) {
	for _, s := range d.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}
func (d Device) capabilities() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0"?><scan:ScannerCapabilities xmlns:scan="%s" xmlns:pwg="%s"><pwg:Version>2.6</pwg:Version><pwg:MakeAndModel>%s</pwg:MakeAndModel><pwg:SerialNumber>SOAPHT-%s</pwg:SerialNumber><scan:UUID>%s</scan:UUID>`, scanNS, pwgNS, xmlText(d.Model), stableUUID(d.URI)[:8], stableUUID(d.URI))
	for _, s := range d.Sources {
		outer, inner := "Platen", "PlatenInputCaps"
		if s.Name == "ADF" {
			outer, inner = "Adf", "AdfSimplexInputCaps"
		}
		fmt.Fprintf(&b, "<scan:%s><scan:%s><scan:MinWidth>%d</scan:MinWidth><scan:MinHeight>%d</scan:MinHeight><scan:MaxWidth>%d</scan:MaxWidth><scan:MaxHeight>%d</scan:MaxHeight><scan:MaxScanRegions>1</scan:MaxScanRegions><scan:SettingProfiles><scan:SettingProfile><scan:ColorModes>", outer, inner, s.MinWidth, s.MinHeight, s.Width, s.Height)
		for _, mode := range s.Modes {
			fmt.Fprintf(&b, "<scan:ColorMode>%s</scan:ColorMode>", mode)
		}
		b.WriteString(`</scan:ColorModes><scan:DocumentFormats><pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat></scan:DocumentFormats><scan:DocumentFormatsExt><scan:DocumentFormatExt>image/jpeg</scan:DocumentFormatExt></scan:DocumentFormatsExt><scan:SupportedResolutions><scan:DiscreteResolutions>`)
		for _, dpi := range s.DPI {
			fmt.Fprintf(&b, "<scan:DiscreteResolution><scan:XResolution>%d</scan:XResolution><scan:YResolution>%d</scan:YResolution></scan:DiscreteResolution>", dpi, dpi)
		}
		fmt.Fprintf(&b, "</scan:DiscreteResolutions></scan:SupportedResolutions></scan:SettingProfile></scan:SettingProfiles></scan:%s></scan:%s>", inner, outer)
	}
	b.WriteString("</scan:ScannerCapabilities>")
	return b.String()
}

// scanimage's coordinate ranges start at zero; they do not expose the backend's
// minimum width/height used by sane_start. Read those from the same SOAP caps.
func (d *Device) applyMinimums(data []byte) error {
	type size struct {
		Width  *int `xml:"Width"`
		Height *int `xml:"Height"`
	}
	mins := map[string]size{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		el, ok := token.(xml.StartElement)
		if !ok || (el.Name.Local != "PlatenMinimumSize" && el.Name.Local != "ADFMinimumSize") {
			continue
		}
		if _, found := mins[el.Name.Local]; found {
			return fmt.Errorf("duplicate minimum size")
		}
		var v size
		if err := dec.DecodeElement(&v, &el); err != nil {
			return err
		}
		mins[el.Name.Local] = v
	}
	for i := range d.Sources {
		s := &d.Sources[i]
		key := "PlatenMinimumSize"
		if s.Name == "ADF" {
			key = "ADFMinimumSize"
		}
		v, ok := mins[key]
		if !ok || v.Width == nil || v.Height == nil || *v.Width < 0 || *v.Height < 0 || *v.Width > 100000 || *v.Height > 100000 {
			return fmt.Errorf("invalid %s", key)
		}
		// SOAP units are 1/1000 inch. One eSCL unit of margin also satisfies
		// HPAIO's strict minimum-height comparison after SANE fixed-point rounding.
		s.MinWidth, s.MinHeight = *v.Width*3/10+1, *v.Height*3/10+1
		if s.MinWidth > s.Width || s.MinHeight > s.Height {
			return fmt.Errorf("minimum exceeds %s bounds", s.Name)
		}
	}
	return nil
}
