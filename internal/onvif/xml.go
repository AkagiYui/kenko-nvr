package onvif

import (
	"bytes"
	"encoding/xml"
)

// findText returns the character data of the first element whose local name
// matches name (namespace-insensitive), or "" if not found.
func findText(data []byte, name string) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == name {
			var s string
			if err := dec.DecodeElement(&s, &se); err == nil {
				return s
			}
			return ""
		}
	}
}

// tokenItem is an ONVIF element carrying a "token" attribute and a Name child,
// such as a media Profile or a PTZ Preset.
type tokenItem struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// tokenizedElements extracts every element whose local name == elemName,
// returning its "token" attribute and the text of its first child element whose
// local name == childName.
func tokenizedElements(data []byte, elemName, childName string) []tokenItem {
	var out []tokenItem
	dec := xml.NewDecoder(bytes.NewReader(data))

	depth := 0
	inElem := false
	elemDepth := 0
	var cur tokenItem
	captureName := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if !inElem && t.Name.Local == elemName {
				inElem = true
				elemDepth = depth
				cur = tokenItem{Token: attr(t, "token")}
			} else if inElem && t.Name.Local == childName && cur.Name == "" {
				captureName = true
			}
		case xml.CharData:
			if captureName {
				cur.Name += string(bytes.TrimSpace([]byte(t)))
			}
		case xml.EndElement:
			if captureName {
				captureName = false
			}
			if inElem && depth == elemDepth {
				out = append(out, cur)
				inElem = false
			}
			depth--
		}
	}
	return out
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
