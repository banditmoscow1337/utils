package browser

import (
	"context"
	"errors"
	"io"

	"net/http"
	"os/exec"
	"runtime"
	"sync"
)

var (
	reqCode string
	reqData []byte
	reqPath string
	wg      sync.WaitGroup
)

func SetTokenServer() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if reqPath != "" {
			if reqCode = r.URL.Query().Get(reqPath); reqCode != "" {
				w.Write([]byte(`<H1 align="center">Close this page</H1>`))
				defer wg.Done()
			}
		} else {
			reqData, _ = io.ReadAll(r.Body)
			defer wg.Done()
		}
	})

}

func GetTokenFromBrowser(url string, path string) (string, []byte, error) {
	srv := &http.Server{Addr: ":25177"}
	wg.Add(1)
	reqCode = ""
	reqData = nil
	reqPath = path
	go func() { //token server
		srv.ListenAndServe()
	}()
	err := OpenBrowser(url)
	if err != nil {
		srv.Shutdown(context.TODO())
		return "", nil, err
	}
	wg.Wait()
	return reqCode, reqData, nil
}

func OpenBrowser(url string) error {
	var how []string
	switch runtime.GOOS {
	case "linux":
		how = []string{"xdg-open"}
	case "windows":
		how = []string{"rundll32", "url.dll,FileProtocolHandler"}
	case "darwin":
		how = []string{"open"}
	default:
		return errors.New("unsupported platform")
	}
	how = append(how, url)
	if err := exec.Command(how[0], how[1:]...).Start(); err != nil {
		return err
	}
	return nil
}
