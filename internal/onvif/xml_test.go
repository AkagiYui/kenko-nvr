package onvif

import "testing"

const getProfilesResponse = `<?xml version="1.0"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
 <env:Body>
  <trt:GetProfilesResponse>
   <trt:Profiles token="Profile_1" fixed="true">
    <tt:Name>MainStream</tt:Name>
   </trt:Profiles>
   <trt:Profiles token="Profile_2">
    <tt:Name>SubStream</tt:Name>
   </trt:Profiles>
  </trt:GetProfilesResponse>
 </env:Body>
</env:Envelope>`

const getStreamURIResponse = `<?xml version="1.0"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
 <env:Body>
  <trt:GetStreamUriResponse>
   <trt:MediaUri>
    <tt:Uri>rtsp://192.168.1.10:554/Streaming/Channels/101</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
   </trt:MediaUri>
  </trt:GetStreamUriResponse>
 </env:Body>
</env:Envelope>`

func TestTokenizedElements(t *testing.T) {
	items := tokenizedElements([]byte(getProfilesResponse), "Profiles", "Name")
	if len(items) != 2 {
		t.Fatalf("expected 2 profiles, got %d: %+v", len(items), items)
	}
	if items[0].Token != "Profile_1" || items[0].Name != "MainStream" {
		t.Errorf("profile 0 = %+v", items[0])
	}
	if items[1].Token != "Profile_2" || items[1].Name != "SubStream" {
		t.Errorf("profile 1 = %+v", items[1])
	}
}

func TestNormalizeXAddr(t *testing.T) {
	cases := map[string]string{
		"192.168.5.19":    "192.168.5.19",
		"192.168.5.19:80": "192.168.5.19:80",
		"http://192.168.5.19/onvif/device_service":  "192.168.5.19",
		"http://192.168.5.19:8899/onvif/device_xxx": "192.168.5.19:8899",
		"192.168.5.19/onvif/device_service":         "192.168.5.19",
		"  192.168.5.18  ":                          "192.168.5.18",
	}
	for in, want := range cases {
		if got := NormalizeXAddr(in); got != want {
			t.Errorf("NormalizeXAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

const notAuthorizedFault = `<?xml version="1.0"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:ter="http://www.onvif.org/ver10/error">
 <env:Body>
  <env:Fault>
   <env:Code>
    <env:Value>env:Sender</env:Value>
    <env:Subcode>
     <env:Value>ter:NotAuthorized</env:Value>
    </env:Subcode>
   </env:Code>
   <env:Reason>
    <env:Text xml:lang="en">Sender not authorized</env:Text>
   </env:Reason>
  </env:Fault>
 </env:Body>
</env:Envelope>`

func TestFaultSummary(t *testing.T) {
	if got, want := faultSummary([]byte(notAuthorizedFault)), "ter:NotAuthorized: Sender not authorized"; got != want {
		t.Errorf("faultSummary(fault) = %q, want %q", got, want)
	}
	// Non-SOAP body (e.g. a proxy error page) falls back to a one-line snippet.
	if got, want := faultSummary([]byte("  Bad\n  Request  ")), "Bad Request"; got != want {
		t.Errorf("faultSummary(plain) = %q, want %q", got, want)
	}
	if got, want := faultSummary(nil), "(empty body)"; got != want {
		t.Errorf("faultSummary(nil) = %q, want %q", got, want)
	}
}

const getDeviceInformationResponse = `<?xml version="1.0" encoding="utf-8" standalone="yes" ?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
 <s:Body>
  <tds:GetDeviceInformationResponse>
   <tds:Manufacturer>Dahua</tds:Manufacturer>
   <tds:Model>DH-2H3400-ADP</tds:Model>
   <tds:FirmwareVersion>2.811.0000013.0.R, Build Date 2023-12-20</tds:FirmwareVersion>
   <tds:SerialNumber>BA07995PHA29A85</tds:SerialNumber>
   <tds:HardwareId>1.00</tds:HardwareId>
  </tds:GetDeviceInformationResponse>
 </s:Body>
</s:Envelope>`

func TestParseDeviceInfo(t *testing.T) {
	got := parseDeviceInfo([]byte(getDeviceInformationResponse))
	want := Info{
		Manufacturer: "Dahua",
		Model:        "DH-2H3400-ADP",
		Firmware:     "2.811.0000013.0.R, Build Date 2023-12-20",
		Serial:       "BA07995PHA29A85",
	}
	if got != want {
		t.Errorf("parseDeviceInfo() = %+v, want %+v", got, want)
	}
}

func TestFindText(t *testing.T) {
	uri := findText([]byte(getStreamURIResponse), "Uri")
	want := "rtsp://192.168.1.10:554/Streaming/Channels/101"
	if uri != want {
		t.Errorf("findText(Uri) = %q, want %q", uri, want)
	}
	if got := findText([]byte(getStreamURIResponse), "Missing"); got != "" {
		t.Errorf("expected empty for missing element, got %q", got)
	}
}
