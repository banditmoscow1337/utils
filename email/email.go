package email

import (
	"encoding/base64"
	"fmt"
	"net/mail"
	"net/smtp"
)

func formatEmailAddress(addr string) string {
	e, err := mail.ParseAddress(addr)
	if err != nil {
		return addr
	}
	return e.String()
}

func composeMimeMail(to string, from string, subject string, body string) []byte {
	header := make(map[string]string)
	header["From"] = formatEmailAddress(from)
	header["To"] = formatEmailAddress(to)
	header["Subject"] = subject
	header["MIME-Version"] = "1.0"
	header["Content-Type"] = "text/plain; charset=\"utf-8\""
	header["Content-Transfer-Encoding"] = "base64"

	message := ""
	for k, v := range header {
		message += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	message += "\r\n" + base64.StdEncoding.EncodeToString([]byte(body))

	return []byte(message)
}

func SendEmail(server, pass, from, to, subject, mesg string) error {
	msg := composeMimeMail(to, from, subject, mesg)
	auth := smtp.PlainAuth("", from, pass, server)
	return smtp.SendMail(server, auth, from, []string{to}, msg)
}
