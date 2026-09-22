package main

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

type Region struct {
	Units  string `xml:"ContentRegionUnits"`
	X      int    `xml:"XOffset"`
	Y      int    `xml:"YOffset"`
	Width  int    `xml:"Width"`
	Height int    `xml:"Height"`
}
type ScanSettings struct {
	XMLName           xml.Name `xml:"ScanSettings"`
	ColorMode         string   `xml:"ColorMode"`
	DocumentFormat    string   `xml:"DocumentFormat"`
	DocumentFormatExt string   `xml:"DocumentFormatExt"`
	XResolution       int      `xml:"XResolution"`
	YResolution       int      `xml:"YResolution"`
	InputSource       string   `xml:"InputSource"`
	Duplex            bool     `xml:"Duplex"`
	ImagesToTransfer  int      `xml:"ImagesToTransfer"`
	Regions           []Region `xml:"ScanRegions>ScanRegion"`
}

func parseSettings(body []byte, d Device) (ScanSettings, error) {
	var s ScanSettings
	if len(body) > 65536 || strings.Contains(string(body), "<!DOCTYPE") || strings.Contains(string(body), "<!ENTITY") {
		return s, fmt.Errorf("invalid XML")
	}
	if e := xml.Unmarshal(body, &s); e != nil {
		return s, e
	}
	if s.InputSource == "" || s.InputSource == "Platen" {
		s.InputSource = "Flatbed"
	}
	if s.InputSource == "Feeder" {
		s.InputSource = "ADF"
	}
	src, ok := d.source(s.InputSource)
	if !ok || s.Duplex {
		return s, fmt.Errorf("unsupported source or duplex")
	}
	if s.DocumentFormatExt != "" {
		s.DocumentFormat = s.DocumentFormatExt
	}
	if s.DocumentFormat == "" {
		s.DocumentFormat = "image/jpeg"
	}
	if s.DocumentFormat != "image/jpeg" {
		return s, fmt.Errorf("unsupported document format")
	}
	if s.XResolution == 0 {
		s.XResolution = src.DPI[0]
		for _, n := range src.DPI {
			if n == 300 {
				s.XResolution = n
			}
		}
	}
	if s.YResolution == 0 {
		s.YResolution = s.XResolution
	}
	valid := false
	for _, n := range src.DPI {
		if n == s.XResolution {
			valid = true
		}
	}
	if !valid || s.YResolution != s.XResolution {
		return s, fmt.Errorf("unsupported resolution")
	}
	if s.ColorMode == "" {
		s.ColorMode = src.Modes[0]
	}
	valid = false
	for _, m := range src.Modes {
		if m == s.ColorMode {
			valid = true
		}
	}
	if !valid {
		return s, fmt.Errorf("unsupported color mode")
	}
	if s.ImagesToTransfer < 0 || s.ImagesToTransfer > 100 {
		return s, fmt.Errorf("unsupported image count")
	}
	if len(s.Regions) == 0 {
		s.Regions = []Region{{Width: src.Width, Height: src.DefaultHeight}}
	}
	if len(s.Regions) != 1 {
		return s, fmt.Errorf("exactly one scan region required")
	}
	r := s.Regions[0]
	if r.Units != "" && r.Units != "escl:ThreeHundredthsOfInches" && r.Units != "scan:ThreeHundredthsOfInches" && r.Units != "ThreeHundredthsOfInches" {
		return s, fmt.Errorf("unsupported region units")
	}
	if r.Width < src.MinWidth || r.Height < src.MinHeight || r.Width < 1 || r.Height < 1 || r.X < 0 || r.Y < 0 || r.X > src.Width || r.Y > src.Height || r.Width > src.Width-r.X || r.Height > src.Height-r.Y {
		return s, fmt.Errorf("region outside source bounds")
	}
	return s, nil
}
func (s ScanSettings) args(uri, dir string) []string {
	mode := "Gray"
	if s.ColorMode == "RGB24" {
		mode = "Color"
	}
	r := s.Regions[0]
	// Quantize each component downward in SANE 16.16 mm before scanimage
	// adds offset + size. Independent decimal rounding could exceed br-y by
	// one fixed-point unit for a crop touching the physical bottom edge.
	mm := func(v int) string {
		fixed := int64(v) * 127 * 65536 / 1500
		return strconv.FormatFloat(float64(fixed)/65536, 'f', 10, 64)
	}
	a := []string{"-d", uri, "--source", s.InputSource, "--mode", mode, "--resolution", strconv.Itoa(s.XResolution), "-l", mm(r.X), "-t", mm(r.Y), "-x", mm(r.Width), "-y", mm(r.Height), "--format=jpeg", "--batch=" + dir + "/page-%04d.jpg", "--batch-print"}
	if s.InputSource == "Flatbed" {
		a = append(a, "--batch-count=1")
	} else if s.ImagesToTransfer > 0 {
		a = append(a, "--batch-count="+strconv.Itoa(s.ImagesToTransfer))
	}
	return a
}
