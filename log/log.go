package log

import (
	"bufio"
	"errors"
	"fmt"
	"os"
)

type Log struct {
	upfunc func(string, string, string)
}

func Init(update func(string, string, string)) (l *Log) {
	l = &Log{upfunc: update}
	return
}

func (l *Log) Warn(fun, mesg string) {
	go l.update("Warn", fun, mesg)
}

func (l *Log) Mesg(fun, mesg string) {
	go l.update("Mesg", fun, mesg)
}

func (l *Log) Error(fun, mesg string) {
	go l.update("Err", fun, mesg)
}

func (l *Log) Fatal(fun, mesg string) {
	l.update("Fatal", fun, mesg)
	os.Exit(1)
}

func (l *Log) update(level, fun, mesg string) {
	fmt.Println("("+level+")", fun+":", mesg)
	if l.upfunc != nil {
		l.upfunc(level, fun, mesg)
	}
}

func GetInput() string {
	input, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return input[:len(input)-1]
}

func (l *Log) IsFatal(err error, fun string) {
	if err != nil {
		l.Fatal(fun, err.Error())
	}
}

func (l *Log) IsErr(err error, fun string) bool {
	if err != nil {
		l.Error(fun, err.Error())
		return true
	}
	return false
}

func Err(err string) error {
	return errors.New(err)
}
