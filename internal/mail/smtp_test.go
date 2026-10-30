package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"i4dcli/internal/fault"
)

func smtpTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, pool
}
func TestSMTPTransactions(t *testing.T) {
	cert, pool := smtpTLS(t)
	for _, mode := range []string{"success", "noTLS", "badCertificate", "rejectRecipient", "unknown", "rejectData"} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			commands := make(chan []string, 1)
			go func() {
				defer server.Close()
				conn := server
				r := bufio.NewReader(conn)
				transcript := []string{}
				defer func() { commands <- transcript }()
				fmt.Fprint(conn, "220 localhost ready\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimSpace(line)
					verb := strings.Fields(line)[0]
					transcript = append(transcript, verb)
					switch verb {
					case "EHLO":
						if mode == "noTLS" {
							fmt.Fprint(conn, "250 localhost\r\n")
						} else {
							fmt.Fprint(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
						}
					case "STARTTLS":
						fmt.Fprint(conn, "220 start TLS\r\n")
						secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
						if secured.Handshake() != nil {
							return
						}
						conn = secured
						r = bufio.NewReader(conn)
					case "AUTH":
						fmt.Fprint(conn, "235 authenticated\r\n")
					case "MAIL":
						fmt.Fprint(conn, "250 sender\r\n")
					case "RCPT":
						if mode == "rejectRecipient" {
							fmt.Fprint(conn, "550 refused\r\n")
						} else {
							fmt.Fprint(conn, "250 recipient\r\n")
						}
					case "RSET":
						fmt.Fprint(conn, "250 reset\r\n")
					case "DATA":
						fmt.Fprint(conn, "354 send message\r\n")
						for {
							body, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if body == ".\r\n" {
								break
							}
						}
						if mode == "unknown" {
							return
						}
						if mode == "rejectData" {
							fmt.Fprint(conn, "550 rejected\r\n")
						} else {
							fmt.Fprint(conn, "250 accepted\r\n")
						}
					case "QUIT":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					}
				}
			}()
			prepared, err := BuildMessage(Compose{From: "sender@example.com", To: []string{"to@example.com"}, Subject: "Subject", Text: "Body"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			roots := pool
			if mode == "badCertificate" {
				roots = x509.NewCertPool()
			}
			result, err := sendConnection(ctx, client, "localhost", SMTPOptions{Username: "sender@example.com", Password: "test-password"}, prepared, &tls.Config{ServerName: "localhost", RootCAs: roots, MinVersion: tls.VersionTLS12})
			if mode == "success" {
				if err != nil || !result.Accepted {
					t.Fatalf("%+v %v", result, err)
				}
			} else {
				if err == nil {
					t.Fatal("unexpected success")
				}
				if mode == "unknown" && fault.Describe(err).Class != "delivery_unknown" {
					t.Fatal(err)
				}
				if mode == "rejectData" && fault.Describe(err).Class == "delivery_unknown" {
					t.Fatal("explicit rejection mislabeled uncertain")
				}
			}
			seen := <-commands
			if mode == "noTLS" || mode == "badCertificate" {
				for _, v := range seen {
					if v == "AUTH" {
						t.Fatal("credentials sent withoutTLS")
					}
				}
			}
			if mode == "rejectRecipient" {
				for _, v := range seen {
					if v == "DATA" {
						t.Fatal("message submitted after recipient rejection")
					}
				}
			}
		})
	}
}
