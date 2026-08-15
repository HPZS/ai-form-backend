// ai-form-backend - AGPL-3.0
package email

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/HPZS/ai-form-backend/config"
)

// fakeSMTP 起一个只会应答 EHLO 的假 SMTP 服务器;advertise 为要额外宣告的扩展(空 = 不宣告)。
func fakeSMTP(t *testing.T, advertise string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 fake ESMTP\r\n")
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						fmt.Fprint(conn, "250-fake\r\n")
						if advertise != "" {
							fmt.Fprintf(conn, "250-%s\r\n", advertise)
						}
						fmt.Fprint(conn, "250 SIZE 10240000\r\n")
					case strings.HasPrefix(cmd, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n
}

func mailer(host string, port int) *Mailer {
	return New(config.SMTPConfig{
		Server: host, Port: port, Account: "a@example.com", Token: "pw",
		SSL: false, SystemName: "测试",
	})
}

// 服务器不支持 STARTTLS 时必须显式报错:继续下去就是把账号口令与整封邮件明文发出去,
// 而静默降级让人永远不知道凭据早就在网上裸奔
func TestSendRefusesPlaintextWhenNoSTARTTLS(t *testing.T) {
	host, port := fakeSMTP(t, "")
	err := mailer(host, port).Send("to@example.com", "标题", "<p>正文</p>")
	if err == nil {
		t.Fatal("不支持 STARTTLS 应报错,绝不能明文发送")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("错误应点明原因,实际: %v", err)
	}
}

// 服务器宣告了 STARTTLS 就应真的去升级(假服务器没有证书,升级必然失败,
// 但失败原因不应是"不支持 STARTTLS")
func TestSendAttemptsSTARTTLSWhenAdvertised(t *testing.T) {
	host, port := fakeSMTP(t, "STARTTLS")
	err := mailer(host, port).Send("to@example.com", "标题", "<p>正文</p>")
	if err == nil {
		t.Fatal("假服务器没有证书,握手应失败")
	}
	if strings.Contains(err.Error(), "不支持 STARTTLS") {
		t.Fatalf("宣告了 STARTTLS 就该尝试升级,实际: %v", err)
	}
}

// 未配置时直接报错,不做任何连接尝试
func TestSendUnconfigured(t *testing.T) {
	if err := New(config.SMTPConfig{}).Send("to@example.com", "t", "b"); err == nil {
		t.Fatal("未配置 SMTP 应报错")
	}
}
