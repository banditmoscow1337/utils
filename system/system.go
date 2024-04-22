package system

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// in milliseconds
func Sleep(timer int) {
	time.Sleep(time.Duration(timer) * time.Millisecond)
}

func GetAppOut(app string, arg []string) string {
	out, err := exec.Command(app, arg...).Output()
	o := string(out)
	if err != nil || len(o) == 0 {
		return ""
	}
	return o[:len(o)-1]
}

func ASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func UnixTime() int64 {
	return time.Now().Unix()
}

func UuidConvert(uuid string) string {
	var num int
	var sym string
	nu := strings.Split(uuid, "-")
	for _, e := range nu {
		stra := strings.Split(e, "")
		for _, r := range stra {
			s, err := strconv.Atoi(r)
			if err != nil {
				sym += r
				continue
			}
			num += s
		}
	}
	return sym + strconv.Itoa(num)
}

func CMD(cmd string, args ...string) {
	cm := exec.Command(cmd, args...)
	cm.Start() //TODO
	cm.Wait()
}

func Do(n int, f func() error) error {
	var e error
	for i := 0; i < n; i += 1 {
		if e = f(); e == nil {
			return e
		}
	}
	return e
}
