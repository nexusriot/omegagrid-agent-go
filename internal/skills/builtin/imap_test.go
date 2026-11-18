package builtin

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func imapTestTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// fakeIMAP serves one scripted session and records the commands it saw.
func fakeIMAP(t *testing.T) (port int, cmds func() []string) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", imapTestTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []string
	msg := "From: =?UTF-8?B?w4lsaXNl?= <elise@example.com>\r\nTo: me@example.com\r\nSubject: Invoice 42\r\n" +
		"Date: Mon, 16 Nov 2026 10:00:00 +0000\r\nContent-Type: multipart/alternative; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Please pay =E2=82=AC10.\r\n--b1\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--b1--\r\n"
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		fmt.Fprint(c, "* OK fake ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			// Literal continuation from the client.
			if strings.HasSuffix(line, "}") {
				i := strings.LastIndex(line, "{")
				n, _ := strconv.Atoi(line[i+1 : len(line)-1])
				fmt.Fprint(c, "+ go ahead\r\n")
				buf := make([]byte, n)
				_, _ = r.Read(buf)
				rest, _ := r.ReadString('\n')
				line = line[:i] + "<lit:" + string(buf) + ">" + strings.TrimRight(rest, "\r\n")
			}
			mu.Lock()
			seen = append(seen, line)
			mu.Unlock()
			tag, cmd, _ := strings.Cut(line, " ")
			switch {
			case strings.HasPrefix(cmd, "LOGIN"):
				fmt.Fprintf(c, "%s OK logged in\r\n", tag)
			case strings.HasPrefix(cmd, "EXAMINE"):
				fmt.Fprintf(c, "* 3 EXISTS\r\n%s OK [READ-ONLY] done\r\n", tag)
			case strings.HasPrefix(cmd, "UID SEARCH"):
				fmt.Fprintf(c, "* SEARCH 3 7 9\r\n%s OK done\r\n", tag)
			case strings.HasPrefix(cmd, "UID FETCH"):
				for _, uid := range []int{7, 9} {
					fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS (\\Seen) INTERNALDATE \"16-Nov-2026 10:00:00 +0000\" BODY[]<0> {%d}\r\n%s)\r\n",
						uid-5, uid, len(msg), msg)
				}
				fmt.Fprintf(c, "%s OK done\r\n", tag)
			case strings.HasPrefix(cmd, "LIST"):
				fmt.Fprintf(c, "* LIST (\\HasNoChildren) \"/\" \"INBOX\"\r\n* LIST () \"/\" Archive\r\n%s OK done\r\n", tag)
			case strings.HasPrefix(cmd, "LOGOUT"):
				fmt.Fprintf(c, "* BYE\r\n%s OK bye\r\n", tag)
				return
			default:
				fmt.Fprintf(c, "%s BAD unknown\r\n", tag)
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestImapSearchReadOnly(t *testing.T) {
	port, cmds := fakeIMAP(t)
	exec := ImapSearchContext(ImapConfig{Host: "127.0.0.1", Port: port, User: "me", Password: `p"w`, InsecureSkipVerify: true})
	m := run(t, exec, map[string]any{"from": "élise", "unread": true, "limit": 2, "include_body": true, "since": "7d"})
	if m["error"] != nil {
		t.Fatalf("%v", m["error"])
	}
	if m["matches"] != 3 || m["returned"] != 2 {
		t.Fatalf("%v", m)
	}
	msgs := m["messages"].([]map[string]any)
	if msgs[0]["uid"] != 9 || msgs[0]["subject"] != "Invoice 42" || !strings.HasPrefix(msgs[0]["from"].(string), "Élise") {
		t.Fatalf("message: %v", msgs[0])
	}
	if msgs[0]["excerpt"] != "Please pay €10." || msgs[0]["unread"] != false {
		t.Fatalf("excerpt: %q", msgs[0]["excerpt"])
	}
	all := strings.Join(cmds(), "\n")
	for _, want := range []string{`LOGIN "me" "p\"w"`, `EXAMINE "INBOX"`, "UID SEARCH CHARSET UTF-8 FROM <lit:élise> UNSEEN SINCE", "UID FETCH 7,9", "BODY.PEEK[]"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, bad := range []string{"SELECT", "STORE", "EXPUNGE"} {
		if strings.Contains(all, " "+bad) {
			t.Errorf("mutating command %s sent", bad)
		}
	}
}

func TestImapMailboxesAndConfig(t *testing.T) {
	port, _ := fakeIMAP(t)
	exec := ImapSearchContext(ImapConfig{Host: "127.0.0.1", Port: port, User: "me", Password: "x", InsecureSkipVerify: true})
	m := run(t, exec, map[string]any{"action": "mailboxes"})
	if fmt.Sprint(m["mailboxes"]) != "[Archive INBOX]" {
		t.Fatalf("%v", m)
	}
	if m := run(t, ImapSearchContext(ImapConfig{}), map[string]any{}); !strings.Contains(fmt.Sprint(m["error"]), "not configured") {
		t.Fatalf("%v", m)
	}
}
