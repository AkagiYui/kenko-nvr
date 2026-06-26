package gb28181

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// gbMessage is a permissive view over the XML body of a GB28181 MESSAGE. Devices
// send several message families (Notify / Response / Query) that share the
// CmdType + SN preamble; we unmarshal into one struct and read the fields that
// matter for the family identified by CmdType.
type gbMessage struct {
	XMLName  xml.Name
	CmdType  string `xml:"CmdType"`
	SN       int    `xml:"SN"`
	DeviceID string `xml:"DeviceID"`

	// Keepalive
	Status string `xml:"Status"`

	// DeviceInfo response
	DeviceName   string `xml:"DeviceName"`
	Manufacturer string `xml:"Manufacturer"`
	Model        string `xml:"Model"`
	Channel      int    `xml:"Channel"`

	// Catalog response
	SumNum     int `xml:"SumNum"`
	DeviceList struct {
		Items []catalogItem `xml:"Item"`
	} `xml:"DeviceList"`
}

// catalogItem is one channel reported in a Catalog response.
type catalogItem struct {
	DeviceID     string `xml:"DeviceID"`
	Name         string `xml:"Name"`
	Manufacturer string `xml:"Manufacturer"`
	Model        string `xml:"Model"`
	Owner        string `xml:"Owner"`
	Status       string `xml:"Status"`
	ParentID     string `xml:"ParentID"`
	Parental     int    `xml:"Parental"`
}

// parseGBMessage decodes a GB28181 XML body, honouring a GB2312/GBK/GB18030
// charset declaration so Chinese channel names survive.
func parseGBMessage(body []byte) (*gbMessage, error) {
	var m gbMessage
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "gb2312", "gbk", "gb18030", "":
			return simplifiedchinese.GBK.NewDecoder().Reader(input), nil
		case "utf-8", "utf8", "us-ascii", "ascii":
			return input, nil
		default:
			return input, nil
		}
	}
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("gb28181: parse xml: %w", err)
	}
	return &m, nil
}

// xmlEscape escapes a value for inclusion in our (UTF-8) outbound XML.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// buildCatalogQuery builds a Catalog query MESSAGE body for a device.
func buildCatalogQuery(deviceID string, sn int) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Query>
<CmdType>Catalog</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>
`, sn, xmlEscape(deviceID)))
}

// buildDeviceInfoQuery builds a DeviceInfo query MESSAGE body for a device.
func buildDeviceInfoQuery(deviceID string, sn int) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Query>
<CmdType>DeviceInfo</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>
`, sn, xmlEscape(deviceID)))
}
