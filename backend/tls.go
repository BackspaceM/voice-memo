package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 浏览器只把 HTTPS 或 localhost 视为安全上下文，HTTP 局域网地址下 getUserMedia 会被禁用。
// 开启 ENABLE_HTTPS 后本服务直接用内置证书提供 HTTPS，无需额外反向代理。
// 自签 CA 可从「设置 → 连接与安全」下载并导入系统信任库，导入后地址栏不再告警、录音可用。
func httpsEnabled() bool {
	v := strings.ToLower(envFirst("ENABLE_HTTPS"))
	return v == "1" || v == "true" || v == "yes"
}

func certNotExpired(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	now := time.Now()
	return now.After(c.NotBefore) && now.Before(c.NotAfter.Add(-7*24*time.Hour))
}

func writePEM(path string, block *pem.Block, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err = f.Chmod(mode); err == nil {
		err = pem.Encode(f, block)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

// ensureTLS 返回 (certFile, keyFile, caFile)。自带证书时 caFile 为空。
func ensureTLS(dataDir string) (string, string, string, error) {
	if cert, key := envFirst("TLS_CERT_FILE"), envFirst("TLS_KEY_FILE"); cert != "" && key != "" {
		if _, err := os.Stat(cert); err != nil {
			return "", "", "", err
		}
		if _, err := os.Stat(key); err != nil {
			return "", "", "", err
		}
		return cert, key, "", nil
	}

	dir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", "", err
	}
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	caPath := filepath.Join(dir, "voice-memo-ca.crt")
	if certNotExpired(certPath) {
		if _, err := os.Stat(keyPath); err == nil {
			if _, err := os.Stat(caPath); err == nil {
				return certPath, keyPath, caPath, nil
			}
		}
	}

	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Voice Memo Suite Local CA", Organization: []string{"Voice Memo Suite"}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return "", "", "", err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	dns := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" {
		dns = append(dns, host)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, item := range strings.Split(envFirst("TLS_SAN"), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			ips = append(ips, ip)
		} else if !containsStr(dns, item) {
			dns = append(dns, item)
		}
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "Voice Memo Suite", Organization: []string{"Voice Memo Suite"}},
		NotBefore:    now.Add(-24 * time.Hour),
		NotAfter:     now.AddDate(3, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return "", "", "", err
	}
	if err = writePEM(caPath, &pem.Block{Type: "CERTIFICATE", Bytes: caDER}, 0644); err != nil {
		return "", "", "", err
	}
	if err = writePEM(certPath, &pem.Block{Type: "CERTIFICATE", Bytes: srvDER}, 0644); err != nil {
		return "", "", "", err
	}
	if err = writePEM(keyPath, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}, 0600); err != nil {
		return "", "", "", err
	}
	log.Printf("已生成自签证书：%s（SAN: %s / %v）", certPath, strings.Join(dns, ", "), ips)
	log.Printf("把 %s 导入设备信任库可消除浏览器告警", caPath)
	return certPath, keyPath, caPath, nil
}

// startRedirect 在独立端口把 HTTP 请求 301 到 HTTPS，便于保留旧的书签。
func startRedirect(port int, httpsPort int) {
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			target := "https://" + net.JoinHostPort(host, strconv.Itoa(httpsPort)) + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}),
	}
	go func() {
		log.Printf("HTTP → HTTPS 重定向监听 %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("重定向服务停止：%v", err)
		}
	}()
}

func tlsMinConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// mobileConfig 生成 iOS / iPadOS 配置描述文件，把自签 CA 装进系统信任库。
// Safari 打开 .mobileconfig 会提示「安装描述文件」，比直接下载 .crt 在 iOS 上可靠得多；
// 描述文件未签名也能安装，但用户仍需在「证书信任设置」里开启完全信任。
func mobileConfig(caPEM []byte) []byte {
	sum := sha256.Sum256(caPEM)
	uuid := func(offset int) string {
		b := make([]byte, 16)
		copy(b, sum[offset:offset+16])
		b[6] = (b[6] & 0x0f) | 0x50
		b[8] = (b[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, caPEM)

	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>PayloadContent</key>
  <array>
    <dict>
      <key>PayloadContent</key>
      <string>`)
	out.Write(escaped.Bytes())
	out.WriteString(`</string>
      <key>PayloadType</key><string>com.apple.security.pem</string>
      <key>PayloadIdentifier</key><string>cn.voice-memo.ca</string>
      <key>PayloadUUID</key><string>` + uuid(0) + `</string>
      <key>PayloadVersion</key><integer>1</integer>
      <key>PayloadDisplayName</key><string>Voice Memo Suite Local CA</string>
    </dict>
  </array>
  <key>PayloadType</key><string>Configuration</string>
  <key>PayloadVersion</key><integer>1</integer>
  <key>PayloadIdentifier</key><string>cn.voice-memo.profile</string>
  <key>PayloadUUID</key><string>` + uuid(8) + `</string>
  <key>PayloadDisplayName</key><string>语音备忘录 本地 CA</string>
  <key>PayloadDescription</key><string>信任 NAS 内置 HTTPS 证书。安装后还需在「设置 → 通用 → 关于本机 → 证书信任设置」启用完全信任。</string>
</dict>
</plist>
`)
	return out.Bytes()
}
