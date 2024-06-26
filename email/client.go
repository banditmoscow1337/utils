package email

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/banditmoscow1337/utils/log"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
)

type MailClient struct {
	Email    string
	Password string
	c        *client.Client
}

func (m *MailClient) Close() error {
	return m.c.Close()
}

func (m *MailClient) GetMessages(subject string) (string, error) {
	mbox, err := m.c.Select("INBOX", false)
	if err != nil {
		return "", err
	}

	if mbox.Messages == 0 {
		return "", log.Err("no message in mailbox")
	}
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(mbox.Messages)

	var section imap.BodySectionName
	items := []imap.FetchItem{section.FetchItem()}

	messages := make(chan *imap.Message, 1)
	go func() {
		if err := m.c.Fetch(seqSet, items, messages); err != nil {
			fmt.Println("GetMessages", err)
			os.Exit(1)
			//ГОВНО ПЕРЕДЕЛЫВАЙ
		}
	}()

	msg := <-messages
	if msg == nil {
		return "", errors.New("server didn't returned message")
	}

	r := msg.GetBody(&section)
	if r == nil {
		return "", errors.New("server didn't returned message body")
	}

	mr, err := mail.CreateReader(r)
	if err != nil {
		return "", err
	}

	header := mr.Header
	ms, err := header.Subject()
	if err != nil {
		return "", err
	}
	if ms != subject {
		return "", errors.New("message with subject not found")
	}

	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}

		switch p.Header.(type) {
		case *mail.InlineHeader:
			b, _ := io.ReadAll(p.Body)
			return string(b), nil
		}
	}

	return "", errors.New("empty message body")
}

func Init(email, password string) (*MailClient, error) {
	var m MailClient
	m.Email = email
	m.Password = password

	c, err := client.DialTLS("imap.mail.ru:993", nil)
	if err != nil {
		return nil, err
	}

	if err := c.Login(m.Email, m.Password); err != nil {
		return nil, err
	}

	m.c = c

	return &m, nil
}
