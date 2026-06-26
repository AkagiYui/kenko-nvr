package gb28181

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// genNonce returns a random hex nonce for a digest challenge.
func genNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; fall back to a fixed-length value.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// wwwAuthenticate builds a Digest WWW-Authenticate challenge header value.
func wwwAuthenticate(realm, nonce string) string {
	return fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, realm, nonce)
}

// parseAuthParams parses the comma-separated key="value" pairs of an
// Authorization (or WWW-Authenticate) header, dropping the leading scheme.
func parseAuthParams(value string) map[string]string {
	value = strings.TrimSpace(value)
	if sp := strings.IndexByte(value, ' '); sp >= 0 {
		value = value[sp+1:] // drop "Digest"
	}
	out := map[string]string{}
	for _, part := range splitAuthList(value) {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(part[:eq]))
		v := strings.Trim(strings.TrimSpace(part[eq+1:]), "\"")
		out[k] = v
	}
	return out
}

// splitAuthList splits on commas that are not inside double quotes.
func splitAuthList(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// verifyDigest checks the response in an Authorization header against the shared
// registration password, per RFC 2617 (MD5, with or without qop=auth).
//
// username/realm/nonce/uri/response are taken from the device's Authorization
// header; method is the request method (REGISTER). The expected HA1 uses the
// realm the device echoed back, which must match the one we challenged with.
func verifyDigest(params map[string]string, method, password string) bool {
	username := params["username"]
	realm := params["realm"]
	nonce := params["nonce"]
	uri := params["uri"]
	response := params["response"]
	if username == "" || nonce == "" || uri == "" || response == "" {
		return false
	}

	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(strings.ToUpper(method) + ":" + uri)

	var expected string
	if qop := params["qop"]; qop != "" {
		expected = md5hex(ha1 + ":" + nonce + ":" + params["nc"] + ":" + params["cnonce"] + ":" + qop + ":" + ha2)
	} else {
		expected = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return strings.EqualFold(expected, response)
}
