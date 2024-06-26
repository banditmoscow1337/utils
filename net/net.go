package net

import (
	"errors"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/net/proxy"
)

func ProxyHttpClient(prx string) (c *http.Client, err error) {
	if len(prx) == 0 {
		err = errors.New("proxy not found")
		return
	}

	proxyUrl, err := url.Parse("http://" + prx)
	if err != nil {
		return
	}

	c = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyUrl)}}
	return
}

func ProxySOCKS5Client(prx, login, pass string) (c *http.Client, err error) {
	if len(prx) == 0 {
		err = errors.New("proxy not found")
		return
	}

	var auth *proxy.Auth

	if len(login) > 0 && len(pass) > 0 {
		auth = &proxy.Auth{
			User:     login,
			Password: pass,
		}
	}

	dialSocksProxy, err := proxy.SOCKS5("tcp", "socks5://"+prx, auth, proxy.Direct)
	if err != nil {
		return
	}

	tr := &http.Transport{Dial: dialSocksProxy.Dial}

	c = &http.Client{
		Transport: tr,
	}

	return
}

func Do(c *http.Client, url, method string, body io.Reader, header http.Header, reader bool) ([]byte, io.Reader, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, nil, err
	}

	req.Header = header

	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}

	if reader {
		return nil, resp.Body, nil
	}

	data, err := io.ReadAll(resp.Body)

	return data, nil, err
}
