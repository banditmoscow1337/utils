package fcm

import (
	"context"

	"utils/log"

	firebase "firebase.google.com/go"
	"firebase.google.com/go/messaging"
	"google.golang.org/api/option"
)

type FCM struct {
	Project string
	Account string
	Key     string
	mesg    *messaging.Client
}

func Init(project, account, key string) (fcm *FCM, err error) {
	if key == "" {
		return nil, log.Err("key invalid")
	}

	if account == "" {
		return nil, log.Err("account invalid")
	}

	if project == "" {
		return nil, log.Err("project invalid")
	}

	fcm = &FCM{project, account, key, nil}

	opt := option.WithCredentialsJSON([]byte(fcm.Key))
	app, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: fcm.Project, ServiceAccountID: fcm.Account}, opt)
	if err != nil {
		return
	}

	fcm.mesg, err = app.Messaging(context.TODO())

	return
}

func (f *FCM) Send(token string, data map[string]string) error {

	_, err := f.mesg.Send(context.TODO(), &messaging.Message{
		Token: token,
		Data:  data,
	})

	return err
}
