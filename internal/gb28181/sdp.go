package gb28181

import (
	"fmt"
	"strings"
)

// makeSSRC builds the 10-digit GB28181 SSRC carried in the SDP "y=" line.
//
// Convention: the leading digit is 0 for a real-time (live) stream and 1 for
// historical playback; the next 5 digits are the regional code taken from the
// channel ID (digits 4-8 of the 20-digit ID); the final 4 digits are a sequence
// number. The device stamps its RTP packets with this SSRC.
func makeSSRC(channelID string, seq int, playback bool) string {
	region := "00000"
	if len(channelID) >= 8 {
		region = channelID[3:8]
	}
	lead := "0"
	if playback {
		lead = "1"
	}
	return fmt.Sprintf("%s%s%04d", lead, region, seq%10000)
}

// buildInviteSDP builds the SDP offer for a live-preview INVITE. The platform is
// the media receiver (recvonly), so recvIP/recvPort name the UDP socket the
// device must send RTP/MPEG-PS to.
func buildInviteSDP(channelID, recvIP string, recvPort int, ssrc string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "v=0\r\n")
	fmt.Fprintf(&b, "o=%s 0 0 IN IP4 %s\r\n", channelID, recvIP)
	fmt.Fprintf(&b, "s=Play\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", recvIP)
	fmt.Fprintf(&b, "t=0 0\r\n")
	fmt.Fprintf(&b, "m=video %d RTP/AVP 96 98 99 97\r\n", recvPort)
	fmt.Fprintf(&b, "a=recvonly\r\n")
	fmt.Fprintf(&b, "a=rtpmap:96 PS/90000\r\n")
	fmt.Fprintf(&b, "a=rtpmap:98 H264/90000\r\n")
	fmt.Fprintf(&b, "a=rtpmap:99 H265/90000\r\n")
	fmt.Fprintf(&b, "a=rtpmap:97 MPEG4/90000\r\n")
	fmt.Fprintf(&b, "y=%s\r\n", ssrc)
	return []byte(b.String())
}
