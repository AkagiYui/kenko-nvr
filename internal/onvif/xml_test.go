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
