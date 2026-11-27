package monitors

import (
	"bytes"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func ip(n int) *int { return &n }

func TestPlanTCPCheck(t *testing.T) {
	send := func(enc, v string) *TCPSend { return &TCPSend{Encoding: enc, Value: v} }
	exp := func(mode, v string) *TCPExpect { return &TCPExpect{Mode: mode, Value: v} }
	tests := []struct {
		name      string
		cur       tcpConfig
		in        TCPSettings
		moved     bool
		wantErr   string // comma-joined fields
		wantDirty bool
		check     func(t *testing.T, cfg tcpConfig, send []byte)
	}{
		{name: "nothing keeps the plain check", in: TCPSettings{}, check: func(t *testing.T, c tcpConfig, _ []byte) {
			if c.SendSet || c.Expect != nil || c.ReadTimeoutSeconds != 0 || c.MaxResponseBytes != 0 {
				t.Fatalf("config changed: %+v", c)
			}
		}},
		{name: "hex send with length prefix and expect", in: TCPSettings{Send: send("hex", "00 00 00 1a 7b"), Expect: exp("contains", `"status":"ok"`)},
			wantDirty: true, check: func(t *testing.T, c tcpConfig, send []byte) {
				if want := []byte{0, 0, 0, 0x1a, 0x7b}; !bytes.Equal(send, want) {
					t.Fatalf("send %x, want %x", send, want)
				}
				if !c.SendSet || c.ReadTimeoutSeconds != 5 || c.MaxResponseBytes != 4096 {
					t.Fatalf("defaults: %+v", c)
				}
			}},
		{name: "base64 and text", in: TCPSettings{Send: send("base64", "AAEC"), Expect: exp("regex", `ok|ready`)}, wantDirty: true},
		{name: "base64 without padding and with spaces", in: TCPSettings{Send: send("base64", " AAEC AA "), Expect: exp("contains", "x")}, wantDirty: true,
			check: func(t *testing.T, _ tcpConfig, send []byte) {
				if want := []byte{0, 1, 2, 0}; !bytes.Equal(send, want) {
					t.Fatalf("send %x, want %x", send, want)
				}
			}},
		{name: "contains longer than the size limit", in: TCPSettings{Expect: exp("contains", "abcdef"), MaxResponseBytes: ptr(5)}, wantErr: "tcp.expect"},
		{name: "text send", in: TCPSettings{Send: send("text", "ping\n"), Expect: exp("contains", "pong")}, wantDirty: true},
		{name: "bad hex", in: TCPSettings{Send: send("hex", "zz"), Expect: exp("contains", "x")}, wantErr: "tcp.send"},
		{name: "bad base64", in: TCPSettings{Send: send("base64", "***"), Expect: exp("contains", "x")}, wantErr: "tcp.send"},
		{name: "bad encoding", in: TCPSettings{Send: send("utf16", "a"), Expect: exp("contains", "x")}, wantErr: "tcp.send"},
		{name: "send too long", in: TCPSettings{Send: send("text", strings.Repeat("a", MaxTCPSendBytes+1)), Expect: exp("contains", "x")}, wantErr: "tcp.send"},
		{name: "send without expect", in: TCPSettings{Send: send("text", "a")}, wantErr: "tcp.expect", wantDirty: true},
		{name: "bad regex", in: TCPSettings{Expect: exp("regex", "(")}, wantErr: "tcp.expect"},
		{name: "bad mode", in: TCPSettings{Expect: exp("glob", "a")}, wantErr: "tcp.expect"},
		{name: "expect too long", in: TCPSettings{Expect: exp("contains", strings.Repeat("a", MaxTCPExpectChars+1))}, wantErr: "tcp.expect"},
		{name: "expect alone is a banner check", in: TCPSettings{Expect: exp("contains", "SSH-2.0")}, check: func(t *testing.T, c tcpConfig, _ []byte) {
			if c.SendSet || c.Expect == nil {
				t.Fatalf("%+v", c)
			}
		}},
		{name: "limits without a rule", in: TCPSettings{ReadTimeoutSeconds: ip(3), MaxResponseBytes: ip(10)}, wantErr: "tcp.readTimeoutSeconds,tcp.maxResponseBytes"},
		{name: "limits out of range", in: TCPSettings{Expect: exp("contains", "x"), ReadTimeoutSeconds: ip(11), MaxResponseBytes: ip(MaxResponseBytesLimit + 1)}, wantErr: "tcp.readTimeoutSeconds,tcp.maxResponseBytes"},
		{name: "limits applied", in: TCPSettings{Expect: exp("contains", "x"), ReadTimeoutSeconds: ip(2), MaxResponseBytes: ip(100)}, check: func(t *testing.T, c tcpConfig, _ []byte) {
			if c.ReadTimeoutSeconds != 2 || c.MaxResponseBytes != 100 {
				t.Fatalf("%+v", c)
			}
		}},
		{name: "empty send removes it", cur: tcpConfig{SendSet: true, Request: "sealed", Expect: &ExpectRule{"contains", "x"}, ReadTimeoutSeconds: 5, MaxResponseBytes: 4096},
			in: TCPSettings{Send: send("hex", "")}, wantDirty: true, wantErr: "", check: func(t *testing.T, c tcpConfig, send []byte) {
				if c.SendSet || send != nil {
					t.Fatalf("%+v", c)
				}
			}},
		{name: "empty expect and send clear the tuning", cur: tcpConfig{SendSet: true, Expect: &ExpectRule{"contains", "x"}, ReadTimeoutSeconds: 5, MaxResponseBytes: 4096},
			in: TCPSettings{Send: send("hex", ""), Expect: exp("contains", "")}, wantDirty: true, check: func(t *testing.T, c tcpConfig, _ []byte) {
				if c.Expect != nil || c.ReadTimeoutSeconds != 0 || c.MaxResponseBytes != 0 {
					t.Fatalf("%+v", c)
				}
			}},
		{name: "unchanged keeps stored send", cur: tcpConfig{SendSet: true, Request: "sealed", Expect: &ExpectRule{"contains", "x"}, ReadTimeoutSeconds: 5, MaxResponseBytes: 4096},
			in: TCPSettings{Expect: exp("contains", "y")}, check: func(t *testing.T, c tcpConfig, _ []byte) {
				if !c.SendSet || c.Request != "sealed" || c.Expect.Value != "y" {
					t.Fatalf("%+v", c)
				}
			}},
		{name: "host moved needs the send again", cur: tcpConfig{SendSet: true, Request: "sealed", Expect: &ExpectRule{"contains", "x"}, ReadTimeoutSeconds: 5, MaxResponseBytes: 4096},
			in: TCPSettings{}, moved: true, wantErr: "tcp.send"},
		{name: "host moved with a new send", cur: tcpConfig{SendSet: true, Request: "sealed", Expect: &ExpectRule{"contains", "x"}, ReadTimeoutSeconds: 5, MaxResponseBytes: 4096},
			in: TCPSettings{Send: send("text", "a")}, moved: true, wantDirty: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var verr validation.Error
			cfg := tc.cur
			send, dirty, _ := planTCPCheck(&verr, &cfg, &tc.in, tc.moved)
			var got []string
			for _, f := range verr.Fields {
				got = append(got, f.Field)
			}
			if strings.Join(got, ",") != tc.wantErr {
				t.Fatalf("errors %v, want %q", got, tc.wantErr)
			}
			if dirty != tc.wantDirty {
				t.Fatalf("dirty %v, want %v", dirty, tc.wantDirty)
			}
			if tc.check != nil {
				tc.check(t, cfg, send)
			}
		})
	}
}

func ptr(v int) *int { return &v }
